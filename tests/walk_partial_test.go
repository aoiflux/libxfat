package test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/aoiflux/libxfat"
)

// Clusters used only by the truncated image, at the far end of the heap so that
// cutting the image short removes nothing else.
const (
	trShort  = 510 // a directory whose records end in its first cluster
	trTail   = 511 // a directory whose first cluster is full, so it needs the next
	trCut    = 512 // the first cluster the truncated image no longer holds
	trBitmap = 513 // where the $BitMap record is pointed, past the cut
)

// buildTruncatedImage returns the superfloppy volume cut short at cluster 512, as an
// acquisition that stopped early leaves it:
//
//	/short      clusters 510-512 by NoFatChain; one record, then end-of-directory
//	/tail       clusters 511-512 by NoFatChain; 511 is full of records, 512 is gone
//
// and with the $BitMap record pointed at cluster 513, so the allocation bitmap is
// gone too and the free-space sweep has nothing to classify clusters by. A real
// formatter puts the bitmap at the front of the heap; this one is at the back
// because that is the only way an image can lose its bitmap and keep its root.
func buildTruncatedImage(t *testing.T) []byte {
	t.Helper()

	image := buildSuperfloppyImage()

	fat := image[sfFatOffsetSector*sfSectorSize : (sfFatOffsetSector+sfFatSizeSectors)*sfSectorSize]
	setFat := func(cluster, next uint32) {
		binary.LittleEndian.PutUint32(fat[cluster*4:cluster*4+4], next)
	}
	clusterStart := func(cluster uint32) int {
		return sfHeapOffsetSector*sfSectorSize + int(cluster-2)*sfClusterSize
	}
	clusterAt := func(cluster uint32) []byte {
		start := clusterStart(cluster)
		return image[start : start+sfClusterSize]
	}

	copy(clusterAt(trShort), sfBuildEntrySet(sfEntrySet{name: "whole.txt", attrs: 0x20}))
	fillDirCluster(clusterAt(trTail), false)

	// The $BitMap record is the second record of the root.
	root := clusterAt(sfRootCluster)
	if root[32] != 0x81 {
		t.Fatalf("root record 1 is type %#x, want the $BitMap record", root[32])
	}
	binary.LittleEndian.PutUint32(root[32+20:32+24], trBitmap)
	setFat(trBitmap, 0xffffffff)

	offset := 0
	for offset < len(root) && root[offset] != 0x00 {
		offset += 32
	}
	for _, s := range []sfEntrySet{
		{name: "short", attrs: 0x10, cluster: trShort, size: 3 * sfClusterSize, noFatChain: true},
		{name: "tail", attrs: 0x10, cluster: trTail, size: 2 * sfClusterSize, noFatChain: true},
	} {
		records := sfBuildEntrySet(s)
		copy(root[offset:], records)
		offset += len(records)
	}

	return image[:clusterStart(trCut)]
}

// TestWalkTruncatedImageIsUnchanged pins what a walk of the truncated image reports
// without the new callbacks, and in doing so states both defects. /tail is listed
// with the 32 children its first cluster holds and no sign that a second cluster
// was never read; /short looks the same, though it lost nothing. And the sweep
// returns nil having swept nothing, because no cluster's allocation was known.
func TestWalkTruncatedImageIsUnchanged(t *testing.T) {
	fs := openSkipImage(t, buildTruncatedImage(t))

	paths := walkPaths(walkAll(t, fs, libxfat.WalkOptions{}))
	tail := 0
	for _, path := range paths {
		if strings.HasPrefix(path, "/tail/") {
			tail++
		}
	}
	if tail != fillDirClusterSets {
		t.Errorf("/tail reported %d children, want the %d its readable cluster holds", tail, fillDirClusterSets)
	}
	if !slices.Contains(paths, "/short/whole.txt") {
		t.Errorf("/short/whole.txt missing; walked %q", paths)
	}

	for _, record := range walkAll(t, fs, libxfat.WalkOptions{IncludeRecovered: true}) {
		if strings.HasPrefix(record.path, libxfat.RecoveredPath+"/") {
			t.Errorf("%s carved from a sweep that had no bitmap to classify clusters by", record.path)
		}
	}
}

// TestWalkOnSkipReportsPartialRead covers the one reason given for a directory the
// walk did descend: it could read only part of it. /tail ran off the end of the
// image before its end-of-directory record. /short ran off too, but only after
// its records had ended, so it lost nothing and is not reported.
func TestWalkOnSkipReportsPartialRead(t *testing.T) {
	fs := openSkipImage(t, buildTruncatedImage(t))

	records, skips := collectWalkAndSkips(t, fs, libxfat.WalkOptions{})

	tail := skipFor(t, skips, "/tail")
	if tail.reason != libxfat.SkipPartialRead {
		t.Errorf("/tail reason = %q, want %q", tail.reason, libxfat.SkipPartialRead)
	}
	if !errors.Is(tail.err, io.EOF) && !errors.Is(tail.err, libxfat.ErrEOF) {
		t.Errorf("/tail error = %v, want the end of the image that cut it short", tail.err)
	}
	if hasSkip(skips, "/short") {
		t.Errorf("/short was reported, but its records ended before the image did; skips %q", skipPaths(skips))
	}
	if len(skips) != 1 {
		t.Errorf("got skips %q, want only /tail", skipPaths(skips))
	}

	// What was read is still reported, after the skip that qualifies it.
	children := 0
	for _, record := range records {
		if strings.HasPrefix(record.path, "/tail/") {
			children++
		}
	}
	if children != fillDirClusterSets {
		t.Errorf("/tail reported %d children, want %d", children, fillDirClusterSets)
	}

	// A partial read has never been a failure, so StopOnReadError does not turn
	// it into one; OnSkip still hears about it.
	_, strict := collectWalkAndSkips(t, fs, libxfat.WalkOptions{StopOnReadError: true})
	if s := skipFor(t, strict, "/tail"); s.reason != libxfat.SkipPartialRead {
		t.Errorf("with StopOnReadError, /tail reason = %q, want %q", s.reason, libxfat.SkipPartialRead)
	}
}

// TestWalkOnSkipReportsPartialDeletedDescent is the deleted half: /split's first
// cluster is free and full, and its second has been taken again, so the descent
// keeps what the first held and stops.
func TestWalkOnSkipReportsPartialDeletedDescent(t *testing.T) {
	fs := openSkipImage(t, buildSkipImage(t))

	records, skips := collectWalkAndSkips(t, fs, libxfat.WalkOptions{
		IncludeDeleted:            true,
		DescendDeletedDirectories: true,
	})

	split := skipFor(t, skips, "/split")
	if split.reason != libxfat.SkipPartialRead {
		t.Errorf("/split reason = %q, want %q", split.reason, libxfat.SkipPartialRead)
	}
	if !errors.Is(split.err, libxfat.ErrDeletedEntry) {
		t.Errorf("/split error = %v, want one wrapping ErrDeletedEntry", split.err)
	}

	children := 0
	for _, record := range records {
		if strings.HasPrefix(record.path, "/split/") {
			children++
		}
	}
	if children != fillDirClusterSets {
		t.Errorf("/split reported %d children, want the %d its free cluster holds", children, fillDirClusterSets)
	}
}

// TestWalkOnRecoveredScanReportsTheSweep covers the sweep's own account of what it
// covered, which the carved entries alone do not carry.
func TestWalkOnRecoveredScanReportsTheSweep(t *testing.T) {
	sweep := func(t *testing.T, fs *libxfat.ExFAT) (*libxfat.RecoveredScanResult, []walkRecord) {
		t.Helper()
		calls := 0
		var got *libxfat.RecoveredScanResult
		var records []walkRecord
		err := fs.WalkWithOptions(context.Background(), libxfat.WalkOptions{
			IncludeRecovered: true,
			OnRecoveredScan: func(result *libxfat.RecoveredScanResult) error {
				calls++
				got = result
				for _, record := range records {
					if strings.HasPrefix(record.path, libxfat.RecoveredPath+"/") {
						t.Errorf("%s was reported before OnRecoveredScan", record.path)
					}
				}
				return nil
			},
		}, func(path string, parent uint32, entry libxfat.Entry) error {
			records = append(records, walkRecord{path, parent, entry})
			return nil
		})
		if err != nil {
			t.Fatalf("WalkWithOptions: %v", err)
		}
		if calls != 1 {
			t.Fatalf("OnRecoveredScan ran %d times, want exactly 1", calls)
		}
		return got, records
	}

	t.Run("whole bitmap", func(t *testing.T) {
		result, records := sweep(t, openSuperfloppy(t, true))
		if result.Truncated || result.ClustersUnclassified != 0 {
			t.Errorf("a readable bitmap reported a truncated sweep: %+v", result)
		}
		if result.ClustersSwept == 0 {
			t.Errorf("the sweep reports reading no clusters: %+v", result)
		}
		carved := slices.ContainsFunc(records, func(r walkRecord) bool {
			return strings.HasPrefix(r.path, libxfat.RecoveredPath+"/")
		})
		if !carved {
			t.Error("the sweep carved nothing from a volume with a known orphan")
		}
	})

	t.Run("bitmap lost to truncation", func(t *testing.T) {
		result, _ := sweep(t, openSkipImage(t, buildTruncatedImage(t)))
		if !result.Truncated {
			t.Errorf("a sweep with no readable bitmap did not report Truncated: %+v", result)
		}
		if result.ClustersUnclassified != sfClusterCount {
			t.Errorf("ClustersUnclassified = %d, want all %d", result.ClustersUnclassified, sfClusterCount)
		}
		if result.ClustersSwept != 0 {
			t.Errorf("ClustersSwept = %d, want 0: nothing was known to be free", result.ClustersSwept)
		}
	})
}

func TestWalkOnRecoveredScanErrorStopsTheWalk(t *testing.T) {
	fs := openSuperfloppy(t, true)

	sentinel := errors.New("refuse the sweep")
	err := fs.WalkWithOptions(context.Background(), libxfat.WalkOptions{
		IncludeRecovered: true,
		OnRecoveredScan:  func(*libxfat.RecoveredScanResult) error { return sentinel },
	}, func(path string, _ uint32, _ libxfat.Entry) error {
		if strings.HasPrefix(path, libxfat.RecoveredPath+"/") {
			t.Errorf("%s was reported after OnRecoveredScan refused the sweep", path)
		}
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WalkWithOptions = %v, want OnRecoveredScan's own error", err)
	}
}
