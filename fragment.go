package libxfat

import (
	"fmt"
	"sort"
)

// Range is one contiguous run of bytes in the image passed to Open.
//
// StartByte is an absolute offset within that io.ReaderAt, and it already
// includes Source.Base: every offset this library computes is relative to the
// start of the reader rather than to the start of the volume, so a Range over a
// volume opened at a partition offset needs no adjustment before it is compared
// against whole-disk byte ranges. Ranges are the unit that lets a caller work
// out which bytes of an image a file occupies without re-walking the FAT.
type Range struct {
	// StartByte is the absolute byte offset of the run within the image.
	StartByte int64 `json:"start_byte"`
	// Length is the number of bytes in the run.
	Length int64 `json:"length"`
	// Sparse is always false on exFAT: the format has no sparse allocation and
	// every run is backed by real clusters. The field exists so that callers can
	// treat exFAT ranges uniformly with filesystems that do have holes.
	//
	// It is not the same thing as never-written: bytes past an entry's
	// ValidDataLength are allocated and readable but were never written. Those
	// are reported by UnwrittenRanges, not here.
	Sparse bool `json:"sparse"`
	// StartCluster is the first cluster of the run, or 0 for a run that is not
	// cluster-addressed at all - the $MBR, $FAT1 and $FAT2 regions.
	StartCluster uint32 `json:"start_cluster"`
	// ClusterCount is the number of clusters in the run, or 0 for a run that is
	// not cluster-addressed.
	ClusterCount uint32 `json:"cluster_count"`
}

// EndByte returns the offset one past the last byte of the run.
func (r Range) EndByte() int64 {
	return r.StartByte + r.Length
}

func (r Range) String() string {
	if r.ClusterCount == 0 {
		return fmt.Sprintf("[%d,%d) %d bytes", r.StartByte, r.EndByte(), r.Length)
	}
	return fmt.Sprintf("[%d,%d) %d bytes, clusters %d-%d",
		r.StartByte, r.EndByte(), r.Length, r.StartCluster, r.StartCluster+r.ClusterCount-1)
}

// FragmentResult reports an entry's runs together with how they were derived.
//
// A forensic caller has to be able to tell a FAT-verified chain from a
// contiguity the library assumed, and both from a contiguity the volume itself
// declared. Every degraded or inferred outcome is flagged rather than hidden, and
// the ranges are returned alongside the flags rather than withheld.
type FragmentResult struct {
	// Ranges are the coalesced runs, in file order. They are usable even when a
	// degradation flag is set: a partial result is returned, never discarded.
	Ranges []Range `json:"ranges"`
	// BytesCovered is the sum of Range.Length.
	BytesCovered int64 `json:"bytes_covered"`
	// ChainWalked is true when Ranges came from an actual FAT chain walk. It is
	// false for a NoFatChain entry, whose FAT entries are undefined, for the
	// region entries, which are not cluster-backed, and for a deleted entry,
	// whose chain has been freed.
	ChainWalked bool `json:"chain_walked"`
	// NoFatChain is true when the entry's stream extension set the NoFatChain
	// flag, so the run was derived from the recorded size without consulting the
	// FAT.
	//
	// Unlike Assumed this is not a guess the library made: the volume declares
	// the stream contiguous and the FAT entries for it are explicitly undefined.
	// Conflating the two would label a fact the volume recorded as a hypothesis.
	NoFatChain bool `json:"no_fat_chain"`
	// ValidBytes is the entry's ValidDataLength: how much of the allocation was
	// ever written. Bytes between it and BytesCovered are located and readable
	// but were never written by this file, and may hold whatever was there
	// before. See UnwrittenRanges.
	ValidBytes int64 `json:"valid_bytes"`
	// Truncated is true when BytesCovered is less than the entry's recorded size.
	Truncated bool `json:"truncated"`
	// Assumed is true when runs were synthesised under FragmentOptions'
	// AssumeContiguous rather than read from the FAT or declared by the volume.
	// Data located through assumed ranges is a hypothesis, not a fact.
	Assumed bool `json:"assumed"`
	// ChainBroken is true when the walk stopped on a free, bad, or out-of-heap
	// FAT entry instead of a proper end-of-chain marker.
	ChainBroken bool `json:"chain_broken"`
	// LoopDetected is true when the chain revisited a cluster. The walk stops at
	// the repeat; the runs before it remain valid.
	LoopDetected bool `json:"loop_detected"`
	// FirstClusterReallocated is true for a deleted entry whose first cluster is
	// now marked in use, meaning its content was most likely overwritten by a
	// later file. Recovery from these ranges is unlikely to succeed. It is false
	// when the allocation bitmap could not be consulted at all, which is a
	// statement about the library's knowledge rather than about the cluster.
	FirstClusterReallocated bool `json:"first_cluster_reallocated"`
	// ClustersWalked counts the clusters located, including those coalesced away.
	ClustersWalked uint32 `json:"clusters_walked"`
}

// FragmentOptions tunes how runs are derived. The zero value is the
// least-fabrication setting: nothing is assumed that the volume did not say.
type FragmentOptions struct {
	// AssumeContiguous reconstructs runs for an entry with no usable FAT chain -
	// in practice a deleted entry that did not record NoFatChain - by assuming
	// that ceil(Size/ClusterSize) clusters follow the first one contiguously. It
	// sets FragmentResult.Assumed, and is off by default so that a caller never
	// receives fabricated offsets it did not ask for.
	AssumeContiguous bool `json:"assume_contiguous"`
	// MaxRuns caps the number of runs returned. Zero means unlimited. Use it to
	// bound work on a hostile image with a pathologically fragmented chain.
	MaxRuns int `json:"max_runs"`
	// MaxClusters caps the clusters walked. Zero defaults to the volume's own
	// cluster count, which is already a hard upper bound on a valid chain.
	MaxClusters uint32 `json:"max_clusters"`
}

// TotalLength sums the lengths of ranges.
func TotalLength(ranges []Range) int64 {
	var total int64
	for _, r := range ranges {
		total += r.Length
	}
	return total
}

// IsFragmented reports whether ranges describe more than one run, which is what
// fragmentation means once the runs have been coalesced.
func IsFragmented(ranges []Range) bool {
	return len(ranges) > 1
}

// Coalesce merges byte-adjacent runs.
//
// The ranges this package produces are already coalesced, because runs are closed
// only where the chain stops being contiguous. It is exported for a caller
// assembling a range list from several sources - intersecting a file against a
// set of changed byte ranges, say - where adjacency can reappear.
func Coalesce(ranges []Range) []Range {
	if len(ranges) < 2 {
		return ranges
	}

	merged := make([]Range, 0, len(ranges))
	current := ranges[0]
	for _, next := range ranges[1:] {
		if current.EndByte() == next.StartByte && current.Sparse == next.Sparse {
			current.Length += next.Length
			current.ClusterCount += next.ClusterCount
			continue
		}
		merged = append(merged, current)
		current = next
	}
	return append(merged, current)
}

// maxCluster is the highest cluster number the heap contains.
func (v *VBR) maxCluster() uint32 {
	return uint32(uint64(v.nbClusters) + FIRST_CLUSTER_NUMBER - 1)
}

// heapEnd is the byte offset one past the last cluster of the heap. It bounds
// anything derived from cluster arithmetic, and is stricter than the image size:
// an image may be larger than the volume inside it.
func (v *VBR) heapEnd() (int64, error) {
	return safeInt64(v.dataAreaStart + uint64(v.nbClusters)*v.clusterSize)
}

// clusterRun converts a run of clusters into a byte range, refusing one that
// would fall outside the cluster heap.
func (v *VBR) clusterRun(start, count uint32) (Range, error) {
	if count == 0 {
		return Range{}, fmt.Errorf("%w: empty cluster run at %d", ErrInvalidCluster, start)
	}
	if !v.isValidCluster(start) {
		return Range{}, fmt.Errorf("%w: %d", ErrInvalidCluster, start)
	}
	last := uint64(start) + uint64(count) - 1
	if last > uint64(v.maxCluster()) {
		return Range{}, fmt.Errorf("%w: run %d+%d ends past cluster %d",
			ErrInvalidCluster, start, count, v.maxCluster())
	}

	offset, err := safeInt64(v.getClusterOffset(start))
	if err != nil {
		return Range{}, err
	}
	length, err := safeInt64(uint64(count) * v.clusterSize)
	if err != nil {
		return Range{}, err
	}

	return Range{
		StartByte:    offset,
		Length:       length,
		StartCluster: start,
		ClusterCount: count,
	}, nil
}

// finalizeRuns trims the ranges so they sum to size and sets BytesCovered.
//
// A size of zero leaves the runs whole. That case is an entry with no recorded
// length rather than an empty one, which never reaches here.
func (v *VBR) finalizeRuns(result *FragmentResult, size int64) {
	if size <= 0 {
		result.BytesCovered = TotalLength(result.Ranges)
		return
	}

	var covered int64
	for i := range result.Ranges {
		if covered >= size {
			result.Ranges = result.Ranges[:i]
			break
		}
		remaining := size - covered
		if result.Ranges[i].Length > remaining {
			result.Ranges[i].Length = remaining
			// The trimmed run still spans whole clusters on disk; report the
			// clusters the bytes actually land in rather than the allocation.
			perCluster := int64(v.clusterSize)
			if perCluster > 0 {
				result.Ranges[i].ClusterCount = uint32((remaining + perCluster - 1) / perCluster)
			}
		}
		covered += result.Ranges[i].Length
	}

	result.BytesCovered = TotalLength(result.Ranges)
	if result.BytesCovered < size {
		result.Truncated = true
	}
}

// rangeOffsetMapper returns a function translating an offset within the
// concatenated content described by ranges into an absolute image offset, or -1
// when the offset falls outside.
//
// It is how a record's position inside a directory is turned into a position
// inside the image, which is correct even when the directory itself is
// fragmented and no single arithmetic expression would do.
func rangeOffsetMapper(ranges []Range) func(int64) int64 {
	starts := make([]int64, len(ranges))
	var total int64
	for i, r := range ranges {
		starts[i] = total
		total += r.Length
	}

	return func(logical int64) int64 {
		if logical < 0 || logical >= total {
			return -1
		}
		i := sort.Search(len(starts), func(i int) bool {
			return starts[i] > logical
		}) - 1
		if i < 0 {
			return -1
		}
		return ranges[i].StartByte + (logical - starts[i])
	}
}

// regionResult describes a synthetic region entry - $MBR, $FAT1, $FAT2 - which
// occupies a fixed byte range outside the cluster heap.
//
// The cluster fields are left zero: the bytes are real, the cluster addressing is
// not. GetClusterList refuses these entries, and correctly so, because it is a
// cluster API. The extent API accepts them because a byte range is exactly what
// it deals in, and a caller intersecting a file against changed image ranges has
// no reason to care which side of the heap boundary the bytes lie on.
func (e *ExFAT) regionResult(entry Entry) (*FragmentResult, error) {
	length, err := safeInt64(entry.dataLen)
	if err != nil {
		return nil, err
	}
	offset, err := safeInt64(entry.regionOffset)
	if err != nil {
		return nil, err
	}
	if e.vbr.size > 0 && offset+length > e.vbr.size {
		return nil, fmt.Errorf("%w: region [%d,%d) past the end of a %d byte image",
			ErrOutOfBounds, offset, offset+length, e.vbr.size)
	}

	validBytes, _ := safeInt64(entry.validDataLen)
	return &FragmentResult{
		Ranges:       []Range{{StartByte: offset, Length: length}},
		BytesCovered: length,
		ValidBytes:   validBytes,
	}, nil
}

// contiguousResult describes an entry whose stream extension set NoFatChain, so
// the volume asserts the allocation is contiguous and the FAT must not be read.
//
// A run that would end past the heap is clamped and flagged Truncated rather than
// refused: the recorded size is an on-disk field, and on a damaged image it is
// exactly the kind of field that is wrong. Clamping locates what can be located.
func (e *ExFAT) contiguousResult(entry Entry, assumed bool) (*FragmentResult, error) {
	size, err := safeInt64(entry.dataLen)
	if err != nil {
		return nil, err
	}
	validBytes, _ := safeInt64(entry.validDataLen)

	count, _ := e.vbr.size2Clusters(entry.dataLen)
	clamped := false
	if available := uint64(e.vbr.maxCluster()) - uint64(entry.entryCluster) + 1; count > available {
		count = available
		clamped = true
	}
	if count == 0 {
		return nil, fmt.Errorf("%w: %d", ErrInvalidCluster, entry.entryCluster)
	}
	if count > uint64(^uint32(0)) {
		return nil, fmt.Errorf("%w: %d clusters", ErrOutOfBounds, count)
	}

	run, err := e.vbr.clusterRun(entry.entryCluster, uint32(count))
	if err != nil {
		return nil, err
	}

	result := &FragmentResult{
		Ranges:         []Range{run},
		NoFatChain:     entry.noFatChain,
		Assumed:        assumed,
		ValidBytes:     validBytes,
		ClustersWalked: uint32(count),
	}
	e.vbr.finalizeRuns(result, size)
	if clamped {
		result.Truncated = true
	}
	return result, nil
}

// walkResult follows the entry's FAT chain, coalescing as it goes.
func (e *ExFAT) walkResult(entry Entry, opts FragmentOptions) (*FragmentResult, error) {
	size, err := safeInt64(entry.dataLen)
	if err != nil {
		return nil, err
	}
	validBytes, _ := safeInt64(entry.validDataLen)

	state := e.vbr.acquireVisit()
	defer e.vbr.releaseVisit(state)

	result := &FragmentResult{ChainWalked: true, ValidBytes: validBytes}
	limits := chainLimits{maxRuns: opts.MaxRuns, maxClusters: opts.MaxClusters}

	outcome, err := e.vbr.walkChainRuns(entry.entryCluster, limits, &state.walkScratch,
		func(run chainRun) error {
			r, err := e.vbr.clusterRun(run.start, run.count)
			if err != nil {
				return err
			}
			result.Ranges = append(result.Ranges, r)
			return nil
		})
	if err != nil {
		return nil, err
	}

	result.ClustersWalked = outcome.clusters
	switch outcome.stop {
	case chainStopBroken:
		result.ChainBroken = true
	case chainStopLoop:
		result.LoopDetected = true
	case chainStopMaxClusters:
		// Exceeding the volume's own cluster count is only possible for a chain
		// that revisits clusters; a caller's own cap is not a claim about the
		// chain at all.
		if outcome.clusterCapped {
			result.LoopDetected = true
		}
	}

	e.vbr.finalizeRuns(result, size)
	if outcome.stop == chainStopMaxRuns || outcome.stop == chainStopMaxClusters {
		result.Truncated = true
	}
	return result, nil
}

// deletedResult locates a deleted entry without walking the FAT.
//
// A deleted entry's chain has been freed. Worse, if its first cluster has since
// been reallocated, walking it would follow the new owner's chain and return
// ranges belonging to an unrelated file. So the chain is never walked.
//
// exFAT differs from FAT here in a way worth taking advantage of. When the
// surviving stream extension recorded NoFatChain, the volume itself declared the
// allocation contiguous, and deletion does not erase that record. That is a fact
// about the layout, not a guess, so it is honoured without AssumeContiguous and
// reported with Assumed false. FAT has no way to state this, which is why its
// sibling library has to offer contiguity as an opt-in hypothesis.
func (e *ExFAT) deletedResult(entry Entry, opts FragmentOptions) (*FragmentResult, error) {
	var result *FragmentResult
	var err error

	switch {
	case entry.noFatChain:
		result, err = e.contiguousResult(entry, false)
	case opts.AssumeContiguous:
		result, err = e.contiguousResult(entry, true)
	default:
		// Locate only what is certain: the first cluster is recorded in the
		// entry, and nothing beyond it can be established without the chain.
		result, err = e.firstClusterOnly(entry)
	}
	if err != nil {
		return nil, err
	}

	// A reallocated first cluster says the content is probably gone. An
	// unreadable bitmap is not evidence either way, so the flag stays false.
	if allocated, bitmapErr := e.IsClusterAllocated(entry.entryCluster); bitmapErr == nil && allocated {
		result.FirstClusterReallocated = true
	}
	return result, nil
}

// firstClusterOnly locates the one cluster a deleted entry still records.
func (e *ExFAT) firstClusterOnly(entry Entry) (*FragmentResult, error) {
	size, err := safeInt64(entry.dataLen)
	if err != nil {
		return nil, err
	}
	validBytes, _ := safeInt64(entry.validDataLen)

	run, err := e.vbr.clusterRun(entry.entryCluster, 1)
	if err != nil {
		return nil, err
	}

	result := &FragmentResult{
		Ranges:         []Range{run},
		ValidBytes:     validBytes,
		ClustersWalked: 1,
	}
	e.vbr.finalizeRuns(result, size)
	return result, nil
}

// IsClusterAllocated reports whether the volume's allocation bitmap marks
// cluster as in use.
//
// It reads the single bitmap byte holding that cluster's bit, located through the
// bitmap stream's own extents so that a fragmented bitmap is handled correctly.
// GetAllocatedClusters and GetFreeClusters read the whole bitmap; this does not.
func (e *ExFAT) IsClusterAllocated(cluster uint32) (bool, error) {
	if !e.vbr.isValidCluster(cluster) {
		return false, fmt.Errorf("%w: %d", ErrInvalidCluster, cluster)
	}
	if err := e.ensureBitmapEntry(); err != nil {
		return false, err
	}

	bitIndex := uint64(cluster) - FIRST_CLUSTER_NUMBER
	logical, err := safeInt64(bitIndex / 8)
	if err != nil {
		return false, err
	}

	// The bitmap's own ranges are taken with flags rather than as an error, so a
	// bitmap that is itself damaged still answers for the clusters it does cover.
	located, err := e.FragmentOffsetsWithOptions(e.bitmapStream(), FragmentOptions{})
	if err != nil {
		return false, err
	}

	at := rangeOffsetMapper(located.Ranges)(logical)
	if at < 0 {
		return false, fmt.Errorf("%w: cluster %d lies past the allocation bitmap",
			ErrOutOfBounds, cluster)
	}

	var b [1]byte
	if err := e.vbr.readAt(b[:], at); err != nil {
		return false, err
	}
	return b[0]&(1<<(bitIndex%8)) != 0, nil
}

// FragmentOffsets returns the absolute image byte ranges occupied by entry.
//
// Consecutive clusters are coalesced, so a contiguous file yields exactly one
// Range and len(result) > 1 means the file is fragmented. The final run is
// trimmed so the ranges sum to the entry's recorded size; the cluster slack past
// that is available from SlackRange, and the never-written tail from
// UnwrittenRanges.
//
// The ranges are gap-free and sum to the recorded size, which is what makes them
// directly intersectable against a set of changed image byte ranges. The
// ValidDataLength boundary deliberately does not split them: it is a metadata
// offset that can fall in the middle of a run, and splitting there would report a
// physically contiguous file as fragmented.
//
// When the located ranges account for fewer bytes than the entry claims, they are
// returned together with an error wrapping ErrTruncatedChain. That is the expected
// outcome for a deleted entry; use FragmentOffsetsWithOptions to receive the
// ranges with the reason as flags instead of as an error.
func (e *ExFAT) FragmentOffsets(entry Entry) ([]Range, error) {
	result, err := e.FragmentOffsetsWithOptions(entry, FragmentOptions{})
	if err != nil {
		return nil, err
	}
	if result.Truncated {
		return result.Ranges, fmt.Errorf("%w: located %d of %d bytes for %q",
			ErrTruncatedChain, result.BytesCovered, entry.dataLen, entry.name)
	}
	return result.Ranges, nil
}

// FragmentOffsetsWithOptions returns entry's runs along with flags describing how
// they were derived.
//
// It returns an error only when no ranges can be produced at all. Every partial
// or degraded outcome is reported through FragmentResult's flags with the usable
// ranges intact, because a file located as far as it can be is more useful than
// an error, and a caller that cannot tell the difference has been misled.
func (e *ExFAT) FragmentOffsetsWithOptions(entry Entry, opts FragmentOptions) (*FragmentResult, error) {
	// An entry with no allocation has no extents. This is not a degraded result:
	// there is nothing to locate, and a zero-length file is a normal thing to be.
	if entry.dataLen == 0 {
		return &FragmentResult{}, nil
	}

	// The region entries are byte ranges outside the cluster heap.
	if entry.isRegion {
		return e.regionResult(entry)
	}

	if entry.entryCluster < uint32(FIRST_CLUSTER_NUMBER) {
		return nil, fmt.Errorf("%w: entry records first cluster %d",
			ErrNoDataClusters, entry.entryCluster)
	}
	if !e.vbr.isValidCluster(entry.entryCluster) {
		return nil, fmt.Errorf("%w: %d", ErrInvalidCluster, entry.entryCluster)
	}

	if entry.IsDeleted() {
		return e.deletedResult(entry, opts)
	}
	if entry.noFatChain {
		return e.contiguousResult(entry, false)
	}
	return e.walkResult(entry, opts)
}

// IsFragmented reports whether entry occupies more than one run.
func (e *ExFAT) IsFragmented(entry Entry) (bool, error) {
	result, err := e.FragmentOffsetsWithOptions(entry, FragmentOptions{})
	if err != nil {
		return false, err
	}
	return IsFragmented(result.Ranges), nil
}

// SlackRange returns the unused tail of entry's last cluster: the bytes between
// the end of its content and the end of the cluster holding it.
//
// Cluster slack is where the remains of whatever previously occupied the cluster
// survive, so it is reported as its own range rather than folded into the content
// extents. The bool is false when there is no slack to report: an entry with no
// allocation, a region entry, a truncated result, or content ending exactly on a
// cluster boundary.
//
// Unlike the sibling FAT library this does not skip directories. Directory
// cluster slack is exactly where deleted directory records survive, and refusing
// to name it would hide the most productive place to look for them.
func (e *ExFAT) SlackRange(entry Entry) (Range, bool, error) {
	if entry.dataLen == 0 || entry.isRegion {
		return Range{}, false, nil
	}

	result, err := e.FragmentOffsetsWithOptions(entry, FragmentOptions{})
	if err != nil {
		return Range{}, false, err
	}
	if result.Truncated || len(result.Ranges) == 0 {
		return Range{}, false, nil
	}

	perCluster := int64(e.vbr.clusterSize)
	if perCluster <= 0 {
		return Range{}, false, nil
	}
	last := result.Ranges[len(result.Ranges)-1]
	used := last.Length % perCluster
	if used == 0 {
		return Range{}, false, nil
	}

	slack := Range{
		StartByte:    last.EndByte(),
		Length:       perCluster - used,
		StartCluster: last.StartCluster + last.ClusterCount - 1,
		ClusterCount: 1,
	}

	// Bounded by the heap rather than the image: an image may be larger than the
	// volume inside it, and the heap end is the stricter limit.
	end, err := e.vbr.heapEnd()
	if err != nil {
		return Range{}, false, err
	}
	if slack.EndByte() > end {
		return Range{}, false, nil
	}
	return slack, true, nil
}

// UnwrittenRanges returns the parts of entry's allocation that lie past its
// ValidDataLength: bytes that are located and readable but were never written by
// this file, and so may still hold what was there before.
//
// This is reported separately from FragmentOffsets rather than as a split in it.
// The boundary is a field in the directory entry, not a property of the layout, so
// splitting a run at it would manufacture a second range over physically
// contiguous bytes and make a contiguous file look fragmented. The region can also
// span several runs - ValidDataLength may be zero on a large allocation - so it is
// not something a flag on the last range could express either.
func (e *ExFAT) UnwrittenRanges(entry Entry) ([]Range, error) {
	if entry.dataLen == 0 || entry.validDataLen >= entry.dataLen {
		return nil, nil
	}

	result, err := e.FragmentOffsetsWithOptions(entry, FragmentOptions{})
	if err != nil {
		return nil, err
	}

	from, err := safeInt64(entry.validDataLen)
	if err != nil {
		return nil, err
	}

	var (
		unwritten []Range
		covered   int64
	)
	for _, r := range result.Ranges {
		end := covered + r.Length
		if end > from {
			skip := int64(0)
			if covered < from {
				skip = from - covered
			}
			part := r
			part.StartByte += skip
			part.Length -= skip
			if part.Length > 0 {
				perCluster := int64(e.vbr.clusterSize)
				if perCluster > 0 && part.ClusterCount != 0 {
					part.StartCluster = r.StartCluster + uint32(skip/perCluster)
					part.ClusterCount = uint32((part.Length + perCluster - 1) / perCluster)
				}
				unwritten = append(unwritten, part)
			}
		}
		covered = end
	}
	return unwritten, nil
}
