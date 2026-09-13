package libxfat

import (
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// countingReaderAt records every read so a test can assert not just what a walk
// returned but what it cost. The point of the topology walk is the reads it does
// not do, and that is invisible to a test which only checks results.
type countingReaderAt struct {
	data  []byte
	reads int
	bytes int
	// dataAreaBytes counts bytes read at or past heapStart.
	dataAreaBytes int
	heapStart     int64
}

func (r *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	r.reads++
	r.bytes += len(p)
	if off >= r.heapStart {
		r.dataAreaBytes += len(p)
	} else if end := off + int64(len(p)); end > r.heapStart {
		r.dataAreaBytes += int(end - r.heapStart)
	}

	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

const (
	tcSectorSize  = 512
	tcClusterSize = 512
	tcFatOffset   = 512
	tcFatSectors  = 1
	tcHeapOffset  = tcFatOffset + tcFatSectors*tcSectorSize
	tcClusters    = 8
)

// newCountingVBR builds a volume whose FAT holds the given links and whose
// cluster heap is real, so a walk that touches file data can be caught doing it.
func newCountingVBR(t *testing.T, links map[uint32]uint32) (VBR, *countingReaderAt) {
	t.Helper()

	size := tcHeapOffset + tcClusters*tcClusterSize
	image := make([]byte, size)
	for cluster, next := range links {
		off := tcFatOffset + int(cluster)*4
		binary.LittleEndian.PutUint32(image[off:off+4], next)
	}
	// Fill the heap so a stray data read is not mistaken for zeroes.
	for i := tcHeapOffset; i < size; i++ {
		image[i] = 0xAB
	}

	reader := &countingReaderAt{data: image, heapStart: tcHeapOffset}
	return VBR{
		dimage:        reader,
		size:          int64(size),
		fatSize:       tcFatSectors,
		sectorSize:    tcSectorSize,
		clusterSize:   tcClusterSize,
		nbClusters:    tcClusters,
		firstFat:      tcFatOffset,
		dataAreaStart: tcHeapOffset,
	}, reader
}

// TestGetChainedClusterListReadsNoFileData is the regression test for the whole
// point of the topology walk. Enumerating a file's clusters used to read every
// byte of the file, so building an extent map for a volume read the volume.
func TestGetChainedClusterListReadsNoFileData(t *testing.T) {
	vbr, reader := newCountingVBR(t, map[uint32]uint32{
		2: 3,
		3: 4,
		4: EXFAT_EOF_START,
	})

	chain, err := vbr.getChainedClusterList(2, 3)
	if err != nil {
		t.Fatalf("getChainedClusterList() error = %v", err)
	}
	if len(chain) != 3 || chain[0] != 2 || chain[1] != 3 || chain[2] != 4 {
		t.Fatalf("getChainedClusterList() = %v, want [2 3 4]", chain)
	}

	if reader.dataAreaBytes != 0 {
		t.Fatalf("read %d bytes of file data to enumerate clusters, want 0",
			reader.dataAreaBytes)
	}
}

// TestCountChainedClustersReadsNoFileData is the same guarantee for counting.
func TestCountChainedClustersReadsNoFileData(t *testing.T) {
	vbr, reader := newCountingVBR(t, map[uint32]uint32{
		2: 3,
		3: 4,
		4: EXFAT_EOF_START,
	})

	count, err := vbr.countChainedClusters(2)
	if err != nil {
		t.Fatalf("countChainedClusters() error = %v", err)
	}
	if count != 3 {
		t.Fatalf("countChainedClusters() = %d, want 3", count)
	}
	if reader.dataAreaBytes != 0 {
		t.Fatalf("read %d bytes of file data to count clusters, want 0",
			reader.dataAreaBytes)
	}
}

// TestWalkChainRunsReusesFatWindow pins the batching. Three consecutive clusters
// share one FAT window, so the walk must not issue a read per cluster.
func TestWalkChainRunsReusesFatWindow(t *testing.T) {
	vbr, reader := newCountingVBR(t, map[uint32]uint32{
		2: 3,
		3: 4,
		4: EXFAT_EOF_START,
	})

	var scratch walkScratch
	outcome, err := vbr.walkChainRuns(2, chainLimits{}, &scratch, func(chainRun) error { return nil })
	if err != nil {
		t.Fatalf("walkChainRuns() error = %v", err)
	}
	if outcome.stop != chainStopEnd {
		t.Fatalf("outcome.stop = %d, want chainStopEnd", outcome.stop)
	}
	if outcome.clusters != 3 {
		t.Fatalf("outcome.clusters = %d, want 3", outcome.clusters)
	}
	if reader.reads != 1 {
		t.Fatalf("walk issued %d reads for a 3-cluster chain, want 1 buffered window",
			reader.reads)
	}
}

// TestWalkChainRunsCoalesces checks the runs handed to emit are maximal, and
// that a backwards link is not folded into the run before it.
func TestWalkChainRunsCoalesces(t *testing.T) {
	cases := map[string]struct {
		links map[uint32]uint32
		start uint32
		want  []chainRun
	}{
		"contiguous is one run": {
			links: map[uint32]uint32{2: 3, 3: 4, 4: EXFAT_EOF_START},
			start: 2,
			want:  []chainRun{{start: 2, count: 3}},
		},
		"gap splits the run": {
			links: map[uint32]uint32{2: 3, 3: 5, 5: EXFAT_EOF_START},
			start: 2,
			want:  []chainRun{{start: 2, count: 2}, {start: 5, count: 1}},
		},
		"three runs": {
			links: map[uint32]uint32{2: 4, 4: 6, 6: EXFAT_EOF_START},
			start: 2,
			want:  []chainRun{{start: 2, count: 1}, {start: 4, count: 1}, {start: 6, count: 1}},
		},
		// A descending link must not merge: an implementation comparing against
		// the previous cluster rather than runStart+runCount would produce one
		// nonsense run spanning backwards.
		"descending link splits": {
			links: map[uint32]uint32{3: 2, 2: EXFAT_EOF_START},
			start: 3,
			want:  []chainRun{{start: 3, count: 1}, {start: 2, count: 1}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			vbr, _ := newCountingVBR(t, tc.links)

			var got []chainRun
			outcome, err := vbr.walkChainRuns(tc.start, chainLimits{}, nil,
				func(run chainRun) error {
					got = append(got, run)
					return nil
				})
			if err != nil {
				t.Fatalf("walkChainRuns() error = %v", err)
			}
			if outcome.stop != chainStopEnd {
				t.Fatalf("outcome.stop = %d, want chainStopEnd", outcome.stop)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("runs = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("runs = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestWalkChainRunsKeepsPartialProgress is the invariant the extent API rests
// on: a chain that goes wrong still reports every run it established, so a
// damaged file is located as far as it can be rather than not at all.
func TestWalkChainRunsKeepsPartialProgress(t *testing.T) {
	cases := map[string]struct {
		links    map[uint32]uint32
		wantStop chainStop
		wantErr  error
	}{
		"free entry mid-chain": {
			links:    map[uint32]uint32{2: 3, 3: 0},
			wantStop: chainStopBroken,
			wantErr:  ErrInvalidCluster,
		},
		"bad cluster marker": {
			links:    map[uint32]uint32{2: 3, 3: EXFAT_BAD_CLUSTER},
			wantStop: chainStopBroken,
			wantErr:  ErrBadCluster,
		},
		"successor past the heap": {
			links:    map[uint32]uint32{2: 3, 3: 100},
			wantStop: chainStopBroken,
			wantErr:  ErrInvalidCluster,
		},
		"two-cycle": {
			links:    map[uint32]uint32{2: 3, 3: 2},
			wantStop: chainStopLoop,
			wantErr:  ErrClusterChainLoop,
		},
		"self-loop": {
			links:    map[uint32]uint32{2: 2},
			wantStop: chainStopLoop,
			wantErr:  ErrClusterChainLoop,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			vbr, _ := newCountingVBR(t, tc.links)

			var clusters uint32
			outcome, err := vbr.walkChainRuns(2, chainLimits{}, nil,
				func(run chainRun) error {
					clusters += run.count
					return nil
				})
			if err != nil {
				t.Fatalf("walkChainRuns() returned a hard error = %v, want a flagged outcome", err)
			}
			if outcome.stop != tc.wantStop {
				t.Fatalf("outcome.stop = %d, want %d", outcome.stop, tc.wantStop)
			}
			// The prefix is evidence and must survive.
			if clusters == 0 {
				t.Fatal("walk discarded every run it had established")
			}
			if clusters != outcome.clusters {
				t.Fatalf("emitted %d clusters, outcome reports %d", clusters, outcome.clusters)
			}
			if got := outcome.legacyErr(); !errors.Is(got, tc.wantErr) {
				t.Fatalf("legacyErr() = %v, want %v", got, tc.wantErr)
			}
		})
	}
}

// TestWalkChainRunsHonoursLimits covers the two caller-supplied bounds, which
// exist so a hostile image cannot turn one call into an unbounded walk. Neither
// is a corrupt chain, so neither maps back to an error.
func TestWalkChainRunsHonoursLimits(t *testing.T) {
	// Alternating gaps, so every cluster is its own run.
	links := map[uint32]uint32{2: 4, 4: 6, 6: 8, 8: EXFAT_EOF_START}

	t.Run("maxRuns", func(t *testing.T) {
		vbr, _ := newCountingVBR(t, links)
		var runs int
		outcome, err := vbr.walkChainRuns(2, chainLimits{maxRuns: 2}, nil,
			func(chainRun) error {
				runs++
				return nil
			})
		if err != nil {
			t.Fatalf("walkChainRuns() error = %v", err)
		}
		if outcome.stop != chainStopMaxRuns {
			t.Fatalf("outcome.stop = %d, want chainStopMaxRuns", outcome.stop)
		}
		if runs != 2 {
			t.Fatalf("emitted %d runs, want 2", runs)
		}
		if err := outcome.legacyErr(); err != nil {
			t.Fatalf("legacyErr() = %v, want nil for a caller-imposed cap", err)
		}
	})

	t.Run("maxClusters", func(t *testing.T) {
		vbr, _ := newCountingVBR(t, links)
		outcome, err := vbr.walkChainRuns(2, chainLimits{maxClusters: 2}, nil,
			func(chainRun) error { return nil })
		if err != nil {
			t.Fatalf("walkChainRuns() error = %v", err)
		}
		if outcome.stop != chainStopMaxClusters {
			t.Fatalf("outcome.stop = %d, want chainStopMaxClusters", outcome.stop)
		}
		if outcome.clusters != 2 {
			t.Fatalf("outcome.clusters = %d, want 2", outcome.clusters)
		}
		if outcome.clusterCapped {
			t.Fatal("clusterCapped set for a caller-supplied limit")
		}
		if err := outcome.legacyErr(); err != nil {
			t.Fatalf("legacyErr() = %v, want nil for a caller-imposed cap", err)
		}
	})
}

// TestFatWindowClampsToFatEnd covers the layout the unit-test fixtures use, and
// that a real small volume has: a FAT shorter than one window. An unclamped
// window read would fail outright.
func TestFatWindowClampsToFatEnd(t *testing.T) {
	if tcFatSectors*tcSectorSize >= fatWindowBytes {
		t.Fatalf("fixture FAT is %d bytes, needs to be smaller than one %d-byte window",
			tcFatSectors*tcSectorSize, fatWindowBytes)
	}

	vbr, _ := newCountingVBR(t, map[uint32]uint32{2: EXFAT_EOF_START})

	var w fatWindow
	next, err := w.next(&vbr, 2)
	if err != nil {
		t.Fatalf("fatWindow.next() error = %v", err)
	}
	if next != EXFAT_EOF_START {
		t.Fatalf("fatWindow.next() = %#x, want %#x", next, EXFAT_EOF_START)
	}
	if w.valid > tcFatSectors*tcSectorSize {
		t.Fatalf("window holds %d bytes, more than the %d-byte FAT",
			w.valid, tcFatSectors*tcSectorSize)
	}
}

// TestFatWindowRejectsClusterWithNoFatEntry keeps nextCluster's contract for a
// cluster whose entry lies past the end of the FAT.
func TestFatWindowRejectsClusterWithNoFatEntry(t *testing.T) {
	vbr, _ := newCountingVBR(t, nil)
	// A 512-byte FAT holds 128 entries, so cluster 200 has none. Widen the heap
	// so isValidCluster does not reject it first.
	vbr.nbClusters = 4096

	var w fatWindow
	if _, err := w.next(&vbr, 200); err == nil {
		t.Fatal("fatWindow.next() on a cluster with no FAT entry returned no error")
	}
}

// newTwoFatVBR builds a TexFAT-shaped volume: two FATs, laid out one after the
// other, holding different chains for the same starting cluster. active selects
// which one the VolumeFlags name.
//
// The two tables disagreeing is the only way to tell which one was read, which is
// why the fixture is built this way rather than by writing one table twice.
func newTwoFatVBR(t *testing.T, active int, first, second map[uint32]uint32) VBR {
	t.Helper()

	const fatBytes = tcFatSectors * tcSectorSize
	secondFatOffset := tcFatOffset + fatBytes
	heapOffset := secondFatOffset + fatBytes

	image := make([]byte, heapOffset+tcClusters*tcClusterSize)
	write := func(base int, links map[uint32]uint32) {
		for cluster, next := range links {
			off := base + int(cluster)*4
			binary.LittleEndian.PutUint32(image[off:off+4], next)
		}
	}
	write(tcFatOffset, first)
	write(secondFatOffset, second)

	var flags uint16
	if active == 1 {
		flags = VOLUME_FLAG_ACTIVE_FAT
	}

	return VBR{
		dimage:        &countingReaderAt{data: image, heapStart: int64(heapOffset)},
		size:          int64(len(image)),
		fatSize:       tcFatSectors,
		sectorSize:    tcSectorSize,
		clusterSize:   tcClusterSize,
		nbClusters:    tcClusters,
		numberOfFats:  2,
		volumeFlags:   flags,
		firstFat:      tcFatOffset,
		dataAreaStart: uint64(heapOffset),
	}
}

// TestActiveFatSelectsTheTableWalked is the regression test for a gap that was
// documented rather than fixed: ActiveFAT reported which table the volume said was
// live, while every chain walk read the first one regardless. On a TexFAT volume
// whose flag selects the second FAT, that meant every chain libxfat reported came
// from the copy the volume had superseded.
func TestActiveFatSelectsTheTableWalked(t *testing.T) {
	// The same starting cluster leads somewhere different in each table.
	firstFat := map[uint32]uint32{2: 3, 3: EXFAT_EOF_START}
	secondFat := map[uint32]uint32{2: 5, 5: 6, 6: EXFAT_EOF_START}

	for _, tc := range []struct {
		name   string
		active int
		want   []uint32
	}{
		{name: "active FAT 0 walks the first table", active: 0, want: []uint32{2, 3}},
		{name: "active FAT 1 walks the second table", active: 1, want: []uint32{2, 5, 6}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vbr := newTwoFatVBR(t, tc.active, firstFat, secondFat)

			chain, err := vbr.getChainedClusterList(2, uint64(len(tc.want)))
			if err != nil {
				t.Fatalf("getChainedClusterList() error = %v", err)
			}
			if len(chain) != len(tc.want) {
				t.Fatalf("chain = %v, want %v", chain, tc.want)
			}
			for i, want := range tc.want {
				if chain[i] != want {
					t.Fatalf("chain = %v, want %v", chain, tc.want)
				}
			}
		})
	}
}

// TestActiveFatIgnoredWithoutASecondTable pins the guard. A volume recording one
// FAT and an active index of 1 is malformed, and honouring the flag there would
// point every walk just past the FAT region - at the cluster heap, read as though
// it were a table of cluster numbers.
func TestActiveFatIgnoredWithoutASecondTable(t *testing.T) {
	vbr, _ := newCountingVBR(t, map[uint32]uint32{2: 3, 3: EXFAT_EOF_START})
	vbr.numberOfFats = 1
	vbr.volumeFlags = VOLUME_FLAG_ACTIVE_FAT

	if got, want := vbr.fatStart(), vbr.firstFat; got != want {
		t.Fatalf("fatStart() = %d, want %d: the only FAT there is", got, want)
	}
	chain, err := vbr.getChainedClusterList(2, 2)
	if err != nil {
		t.Fatalf("getChainedClusterList() error = %v", err)
	}
	if len(chain) != 2 || chain[0] != 2 || chain[1] != 3 {
		t.Fatalf("chain = %v, want [2 3]", chain)
	}
}
