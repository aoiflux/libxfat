package libxfat

import (
	"fmt"
	"io"
)

// fatWindowBytes is the span of the FAT buffered at once: one page, 1024
// entries.
//
// Walking a chain one four-byte ReadAt at a time is a syscall per cluster, which
// on a fragmented multi-gigabyte file dominates everything else the walk does.
// Because 4 divides this evenly, no entry ever straddles two windows.
const fatWindowBytes = 4096

// maxSeenTracked bounds the loop-detection set. A chain longer than this still
// terminates - walkChainRuns counts clusters against the volume's own cluster
// count regardless - but past this point the walk can no longer say whether it
// stopped because the chain looped or because it ran on too long, and it reports
// the weaker claim. A 4 TiB volume at 512-byte clusters has eight billion
// clusters, so an unbounded set is not an option.
const maxSeenTracked = 1 << 20

// fatWindow buffers one aligned span of the FAT.
//
// It is per-walk state: it holds a cursor, so it can no more be shared between
// two concurrent walks than the cluster buffer beside it can.
type fatWindow struct {
	buf []byte
	// rel is the FAT-relative offset of buf[0]. Alignment is computed against
	// the FAT's own start rather than the image's, so the first window begins
	// exactly where the FAT does.
	rel   uint64
	valid int // bytes filled; the FAT's final window may be short
	ready bool
}

func (w *fatWindow) invalidate() { w.ready = false }

func (w *fatWindow) covers(entryOff uint64) bool {
	return w.ready && entryOff >= w.rel && entryOff+4 <= w.rel+uint64(w.valid)
}

// next returns the FAT entry for cluster.
//
// The preconditions and error values are those of nextCluster, which this
// replaces on the walking paths: an out-of-heap cluster is ErrInvalidCluster, a
// cluster with no FAT entry at all is reported as such, and a bad-cluster marker
// is ErrBadCluster rather than a cluster number.
func (w *fatWindow) next(v *VBR, cluster uint32) (uint32, error) {
	if !v.isValidCluster(cluster) {
		return 0, fmt.Errorf("%w: %d", ErrInvalidCluster, cluster)
	}

	fatBytes := v.fatBytes()
	if uint64(cluster) >= fatBytes/4 {
		return 0, fmt.Errorf("cluster out of fat: %d", cluster)
	}

	entry, err := w.entryAt(v, uint64(cluster)*4, fatBytes)
	if err != nil {
		return 0, err
	}

	next := unpackLELong(entry) & EXFAT_CLUSTER_MASK
	if next == EXFAT_BAD_CLUSTER {
		return 0, ErrBadCluster
	}
	return next, nil
}

// entryAt returns the four bytes of the FAT at the FAT-relative offset entryOff,
// loading the window holding them when they are not already buffered.
func (w *fatWindow) entryAt(v *VBR, entryOff, fatBytes uint64) ([]byte, error) {
	if !w.covers(entryOff) {
		if err := w.load(v, entryOff, fatBytes); err != nil {
			return nil, err
		}
		if !w.covers(entryOff) {
			// The image ends inside the FAT, short of this entry. The chain
			// cannot be followed further, and saying so is the honest answer.
			return nil, io.ErrUnexpectedEOF
		}
	}
	pos := int(entryOff - w.rel)
	return w.buf[pos : pos+4], nil
}

func (w *fatWindow) load(v *VBR, entryOff, fatBytes uint64) error {
	rel := entryOff - entryOff%fatWindowBytes

	// The window is clamped to the FAT rather than read blind. Reading past the
	// FAT's end would fail outright on a volume whose whole FAT is smaller than
	// one window, which is both a legitimate layout and what the unit tests
	// build.
	span := uint64(fatWindowBytes)
	if rel >= fatBytes {
		return io.ErrUnexpectedEOF
	}
	if rel+span > fatBytes {
		span = fatBytes - rel
	}

	if w.buf == nil {
		w.buf = make([]byte, fatWindowBytes)
	}

	off, err := safeInt64(v.fatStart() + rel)
	if err != nil {
		return err
	}

	n, err := v.readSome(w.buf[:span], off)
	if err != nil {
		w.invalidate()
		return err
	}

	w.rel = rel
	w.valid = n
	w.ready = true
	return nil
}

// walkScratch is what a topology walk needs.
//
// It deliberately excludes the cluster buffer: enumerating a file's extents must
// not allocate one, because on a volume with 32 MiB clusters that is 32 MiB held
// to read no file data at all.
type walkScratch struct {
	seen map[uint32]struct{}
	fat  fatWindow
}

// loopSet returns the scratch's loop-detection set, emptied and ready. clear
// keeps the buckets, which is the point of pooling it.
func (s *walkScratch) loopSet() map[uint32]struct{} {
	if s.seen == nil {
		s.seen = make(map[uint32]struct{})
		return s.seen
	}
	clear(s.seen)
	return s.seen
}

// chainRun is a maximal span of consecutively numbered clusters within a chain.
type chainRun struct {
	start uint32
	count uint32
}

// chainStop says why a walk ended. Every value but chainStopEnd describes a
// chain that did not reach a proper end-of-chain marker, and in every one of
// those cases the runs established before the stop have already been emitted.
type chainStop uint8

const (
	chainStopEnd    chainStop = iota // a proper end-of-chain marker
	chainStopBroken                  // a free, bad, or out-of-heap entry, or a read error
	chainStopLoop                    // the chain revisited a cluster
	chainStopMaxRuns
	chainStopMaxClusters
	chainStopEmit // emit asked to stop
)

// chainLimits bounds a walk. The zero value means "the volume's own cluster
// count", which is already a hard upper bound on any correct chain.
type chainLimits struct {
	maxRuns     int
	maxClusters uint32
}

type chainOutcome struct {
	stop     chainStop
	clusters uint32
	runs     int
	// cause carries the underlying read or FAT error behind chainStopBroken, so
	// that a caller translating back to an error reports the real one.
	cause error
	// clusterCapped records that maxClusters was the volume's cluster count
	// rather than a limit the caller chose. Exceeding it then means the chain
	// cannot be valid, which is a stronger statement than hitting a caller's cap.
	clusterCapped bool
}

// legacyErr translates an outcome into the error the pre-extent cluster APIs
// have always returned.
//
// This is the split the refactor rests on: errors at the cluster-list and
// data-walk boundary, flags at the extent boundary. One engine, two
// translations, so that getChainedClusterList keeps failing exactly where it
// always failed while FragmentOffsets keeps the runs and describes what
// happened instead.
func (o chainOutcome) legacyErr() error {
	switch o.stop {
	case chainStopEnd, chainStopEmit, chainStopMaxRuns:
		return nil
	case chainStopLoop:
		return ErrClusterChainLoop
	case chainStopMaxClusters:
		// Running past the volume's own cluster count is only possible for a
		// chain that revisits clusters, which is what the old walk called it.
		if o.clusterCapped {
			return ErrClusterChainLoop
		}
		return nil
	default:
		if o.cause != nil {
			return o.cause
		}
		return fmt.Errorf("%w: chain ended without an end-of-chain marker", ErrInvalidCluster)
	}
}

// walkChainRuns walks the FAT chain starting at start, reading only the
// four-byte FAT entries, and calls emit once per maximal run of consecutively
// numbered clusters.
//
// It does not discard work. A chain that breaks, loops, or hits a limit has
// already handed emit every run it established; why it stopped is in the
// outcome, not in an error. An error is returned only for a precondition the
// caller got wrong - a start cluster outside the heap, a nil reader - or verbatim
// from emit, so that a sentinel emit uses to stop early survives errors.Is.
func (v *VBR) walkChainRuns(start uint32, limits chainLimits,
	scratch *walkScratch, emit func(chainRun) error) (chainOutcome, error) {

	var out chainOutcome

	if scratch == nil {
		scratch = &walkScratch{}
	}
	if !v.isValidCluster(start) {
		return out, fmt.Errorf("%w: %d", ErrInvalidCluster, start)
	}

	maxClusters := limits.maxClusters
	if maxClusters == 0 || maxClusters > v.nbClusters {
		maxClusters = v.nbClusters
		out.clusterCapped = true
	}

	seen := scratch.loopSet()
	window := &scratch.fat
	window.invalidate()

	var (
		runStart = start
		runCount uint32
	)

	// flush closes the run under construction. It is called on every exit path,
	// including the degraded ones: a run that was established is evidence
	// whatever went wrong after it.
	flush := func() error {
		if runCount == 0 {
			return nil
		}
		run := chainRun{start: runStart, count: runCount}
		runCount = 0
		out.runs++
		return emit(run)
	}

	cluster := start
	seen[cluster] = struct{}{}

	for {
		if out.clusters >= maxClusters {
			out.stop = chainStopMaxClusters
			break
		}

		if runCount > 0 && cluster != runStart+runCount {
			if err := flush(); err != nil {
				return out, err
			}
			if limits.maxRuns > 0 && out.runs >= limits.maxRuns {
				out.stop = chainStopMaxRuns
				// cluster is deliberately not counted: it would open a run that
				// is never emitted, and ClustersWalked must describe what the
				// caller was actually told about.
				return out, nil
			}
		}
		if runCount == 0 {
			runStart = cluster
		}
		runCount++
		out.clusters++

		next, err := window.next(v, cluster)
		if err != nil {
			out.stop = chainStopBroken
			out.cause = err
			break
		}

		if next >= EXFAT_EOF_START && next <= EXFAT_EOF_END {
			out.stop = chainStopEnd
			break
		}
		if !v.isValidCluster(next) {
			out.stop = chainStopBroken
			out.cause = fmt.Errorf("%w: %d", ErrInvalidCluster, next)
			break
		}
		if len(seen) < maxSeenTracked {
			if _, dup := seen[next]; dup {
				out.stop = chainStopLoop
				break
			}
			seen[next] = struct{}{}
		}

		cluster = next
	}

	if err := flush(); err != nil {
		return out, err
	}
	return out, nil
}
