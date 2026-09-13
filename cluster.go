package libxfat

import (
	"errors"
	"fmt"
)

var errStopClusterWalk = errors.New("stop cluster walk")

func (v *VBR) getClusterOffset(cluster uint32) uint64 {
	clusterNumber := uint64(cluster) - FIRST_CLUSTER_NUMBER
	offset := v.dataAreaStart + clusterNumber*v.clusterSize
	return offset
}

func (v *VBR) isValidCluster(cluster uint32) bool {
	if cluster < uint32(FIRST_CLUSTER_NUMBER) {
		return false
	}
	return uint64(cluster) < uint64(v.nbClusters)+FIRST_CLUSTER_NUMBER
}

func (v *VBR) readClusterInto(cluster uint32, buf []byte) error {
	if uint64(len(buf)) != v.clusterSize {
		return fmt.Errorf("invalid cluster buffer size: got %d want %d", len(buf), v.clusterSize)
	}
	if !v.isValidCluster(cluster) {
		return fmt.Errorf("%w: %d", ErrInvalidCluster, cluster)
	}

	offset, err := safeInt64(v.getClusterOffset(cluster))
	if err != nil {
		return err
	}

	return v.readAt(buf, offset)
}

// acquireVisit takes per-walk scratch from the pool, or builds it on first use.
// The returned state is exclusively the caller's until it is released.
func (v *VBR) acquireVisit() *visitState {
	if v.visitPool != nil {
		if state, ok := v.visitPool.Get().(*visitState); ok && state != nil {
			state.reset(v)
			return state
		}
	}
	return &visitState{}
}

func (v *VBR) releaseVisit(state *visitState) {
	if v.visitPool == nil {
		return
	}
	state.reset(v)
	v.visitPool.Put(state)
}

func (v *VBR) visitContiguousClusters(start uint32, count uint64, visitor func(cluster uint32, data []byte) error) error {
	if count == 0 {
		return nil
	}

	state := v.acquireVisit()
	defer v.releaseVisit(state)

	buf := state.ensureBuf(v)
	cluster := start
	for i := uint64(0); i < count; i++ {
		if err := v.readClusterInto(cluster, buf); err != nil {
			return err
		}
		if err := visitor(cluster, buf); err != nil {
			return err
		}
		cluster++
	}

	return nil
}

// visitFatChain walks a FAT chain and hands each cluster's data to visitor.
//
// It is the data-reading layer over walkChainRuns, which is what keeps loop
// detection, the cluster-count bound and the end-of-chain test in one place
// rather than in two implementations that drift. The error contract is
// unchanged: a chain that loops or breaks fails, rather than returning what it
// managed to read. Callers that want the recovered prefix instead go through the
// extent API, which reads the same outcome as flags.
//
// The set of clusters the visitor sees before a failure is also unchanged. A
// cluster is emitted as part of a run before its own FAT entry is read, exactly
// as the previous implementation visited a cluster before looking up its
// successor; only the order of the underlying reads differs.
func (v *VBR) visitFatChain(start uint32, visitor func(cluster uint32, data []byte) error) error {
	state := v.acquireVisit()
	defer v.releaseVisit(state)

	outcome, err := v.walkChainRuns(start, chainLimits{}, &state.walkScratch,
		func(run chainRun) error {
			buf := state.ensureBuf(v)
			for i := uint32(0); i < run.count; i++ {
				cluster := run.start + i
				if err := v.readClusterInto(cluster, buf); err != nil {
					return err
				}
				if err := visitor(cluster, buf); err != nil {
					return err
				}
			}
			return nil
		})
	if err != nil {
		return err
	}
	return outcome.legacyErr()
}

func (v *VBR) visitEntryData(entry Entry, visitor func(cluster uint32, data []byte) error) error {
	if entry.dataLen == 0 {
		return nil
	}

	remaining := entry.dataLen
	visitChunk := func(cluster uint32, data []byte) error {
		chunk := data
		if uint64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		remaining -= uint64(len(chunk))
		if err := visitor(cluster, chunk); err != nil {
			return err
		}
		if remaining == 0 {
			return errStopClusterWalk
		}
		return nil
	}

	if entry.IsContiguous() {
		sizeInClusters, _ := v.size2Clusters(entry.dataLen)
		err := v.visitContiguousClusters(entry.entryCluster, sizeInClusters, visitChunk)
		if errors.Is(err, errStopClusterWalk) {
			return nil
		}
		return err
	}

	err := v.visitFatChain(entry.entryCluster, visitChunk)
	if errors.Is(err, errStopClusterWalk) {
		return nil
	}
	return err
}

func (v *VBR) size2Clusters(size uint64) (uint64, uint32) {
	sizeInClusters := size / v.clusterSize
	remainder := size % v.clusterSize
	if remainder > 0 {
		sizeInClusters++
	}
	return sizeInClusters, uint32(remainder)
}

func (v *VBR) getClusterList(entry Entry) ([]uint32, uint64, error) {
	// Region entries ($MBR, $FAT1, $FAT2) are byte ranges outside the cluster
	// heap. Running them through the arithmetic below derives a cluster number
	// from a first cluster of 0 and then complains about that derived value,
	// which tells the caller nothing useful.
	if entry.isRegion {
		return nil, 0, ErrNoClusterMapping
	}

	if entry.dataLen == 0 {
		return nil, 0, nil
	}

	// Report the cluster that is actually wrong, not one computed from it.
	if !v.isValidCluster(entry.entryCluster) {
		return nil, 0, fmt.Errorf("%w: %d", ErrInvalidCluster, entry.entryCluster)
	}

	sizeInClusters, remainder := v.size2Clusters(entry.dataLen)

	// A FAT-chained entry's cluster list comes from the chain, not from the size,
	// so building the contiguous range first only to replace it is a whole
	// []uint32 allocated and dropped for every fragmented file. Pass the size down
	// as a capacity hint instead, where it is worth something.
	var (
		clusterList []uint32
		err         error
	)
	if entry.IsContiguous() {
		clusterList = getRange(entry.entryCluster, sizeInClusters)
	} else {
		clusterList, err = v.getChainedClusterList(entry.entryCluster, sizeInClusters)
		if err != nil {
			return nil, 0, err
		}
	}

	// The tail cluster is read to confirm the mapping actually resolves to
	// readable data before it is handed out; the bytes themselves are not wanted.
	// It is the one data read left on this path - walking the chain now touches
	// only the FAT - and it is kept because a cluster list that cannot be read is
	// worth failing on here rather than somewhere further from the cause.
	state := v.acquireVisit()
	defer v.releaseVisit(state)

	latestCluster := clusterList[len(clusterList)-1]
	if err = v.readClusterInto(latestCluster, state.ensureBuf(v)); err != nil {
		return nil, 0, err
	}

	filetail := v.clusterSize
	if remainder > 0 {
		filetail = uint64(remainder)
	}

	return clusterList, filetail, nil
}

// maxClusterListSeed bounds the capacity getChainedClusterList reserves up
// front. The seed is derived from a size read off the disk, and on a forensic
// image a size is exactly the kind of field that is wrong or hostile, so it must
// never name an allocation on its own. Past this point the doubling growth is
// amortised well enough that the seed is not worth the exposure.
const maxClusterListSeed = 4096

// getChainedClusterList walks a FAT chain from cluster and returns every cluster
// in it.
//
// sizeHint is the chain length the entry's size implies, and it only seeds
// capacity: the walk alone decides the contents, so a hint that is too small
// costs an append growth and one that is too large costs some slack, and neither
// can change the answer. Callers with no size to offer pass 0.
func (v *VBR) getChainedClusterList(cluster uint32, sizeHint uint64) ([]uint32, error) {
	var clusterList []uint32
	if sizeHint > 0 {
		// A chain longer than the volume has clusters cannot be valid, so that
		// is the most a correct result can hold.
		clusterList = make([]uint32, 0, min(sizeHint, uint64(v.nbClusters), maxClusterListSeed))
	}

	state := v.acquireVisit()
	defer v.releaseVisit(state)

	// Only the FAT is read here. The previous implementation went through
	// visitFatChain, which reads every cluster of the file and handed the data
	// to a visitor that dropped it - so enumerating a file's clusters read the
	// whole file, and enumerating a volume's read the whole volume.
	outcome, err := v.walkChainRuns(cluster, chainLimits{}, &state.walkScratch,
		func(run chainRun) error {
			for i := uint32(0); i < run.count; i++ {
				clusterList = append(clusterList, run.start+i)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	if err := outcome.legacyErr(); err != nil {
		return nil, err
	}
	return clusterList, nil
}

func (v *VBR) countChainedClusters(cluster uint32) (int, error) {
	state := v.acquireVisit()
	defer v.releaseVisit(state)

	// Counting a chain reads only the FAT; it used to read every byte of the
	// file to arrive at the same number.
	outcome, err := v.walkChainRuns(cluster, chainLimits{}, &state.walkScratch,
		func(chainRun) error { return nil })
	if err != nil {
		return -1, err
	}
	if err := outcome.legacyErr(); err != nil {
		return -1, err
	}
	return int(outcome.clusters), nil
}

func (v *VBR) countClusters(entry Entry) (int, error) {
	// Region entries ($MBR, $FAT1, $FAT2) are byte ranges outside the cluster
	// heap, so they have no clusters to count. Dividing their size by the
	// cluster size produces a number that looks like an answer and refers to
	// nothing - and contradicts getClusterList, which refuses them outright.
	// Locate them with Entry.RegionOffset and Size instead.
	if entry.isRegion {
		return 0, ErrNoClusterMapping
	}

	if entry.dataLen == 0 {
		return 0, nil
	}
	if entry.IsContiguous() {
		sizeInClusters, _ := v.size2Clusters(entry.dataLen)
		return int(sizeInClusters), nil
	}
	return v.countChainedClusters(entry.entryCluster)
}
