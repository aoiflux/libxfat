package libxfat

import (
	"errors"
	"fmt"
	"io"
	"os"
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

func (v *VBR) readClusters(cluster uint32, nbcluster uint64) ([]byte, error) {
	if nbcluster == 0 {
		return []byte{}, nil
	}
	if !v.isValidCluster(cluster) {
		return nil, fmt.Errorf("%w: %d", ErrInvalidCluster, cluster)
	}
	clusterIndex := uint64(cluster) - FIRST_CLUSTER_NUMBER
	if clusterIndex+nbcluster > uint64(v.nbClusters) {
		return nil, fmt.Errorf("out of range: cluster=%d count=%d", cluster, nbcluster)
	}

	offset, err := safeInt64(v.getClusterOffset(cluster))
	if err != nil {
		return nil, err
	}

	clusterdata := make([]byte, v.clusterSize*nbcluster)
	err = v.readAt(clusterdata, offset)

	return clusterdata, err
}

func (v *VBR) nextCluster(cluster uint32) (uint32, error) {
	if !v.isValidCluster(cluster) {
		return 0, fmt.Errorf("%w: %d", ErrInvalidCluster, cluster)
	}

	fatEntries := (uint64(v.fatSize) * uint64(v.sectorSize)) / 4
	if uint64(cluster) >= fatEntries {
		return 0, fmt.Errorf("cluster out of fat: %d", cluster)
	}

	fatBase, err := safeInt64(v.firstFat)
	if err != nil {
		return 0, err
	}
	offset := fatBase + (int64(cluster) * 4)

	var data [4]byte
	if err := v.readAt(data[:], offset); err != nil {
		return 0, err
	}

	nextCluster := unpackLELong(data[:]) & EXFAT_CLUSTER_MASK
	if nextCluster == EXFAT_BAD_CLUSTER {
		return 0, ErrBadCluster
	}
	return nextCluster, nil
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

func (v *VBR) visitContiguousClusters(start uint32, count uint64, visitor func(cluster uint32, data []byte) error) error {
	if count == 0 {
		return nil
	}

	buf := make([]byte, v.clusterSize)
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

func (v *VBR) visitFatChain(start uint32, visitor func(cluster uint32, data []byte) error) error {
	if !v.isValidCluster(start) {
		return fmt.Errorf("%w: %d", ErrInvalidCluster, start)
	}

	buf := make([]byte, v.clusterSize)
	seen := make(map[uint32]struct{})
	cluster := start
	visited := uint32(0)

	for {
		if !v.isValidCluster(cluster) {
			return fmt.Errorf("%w: %d", ErrInvalidCluster, cluster)
		}
		if _, ok := seen[cluster]; ok {
			return ErrClusterChainLoop
		}
		seen[cluster] = struct{}{}
		if visited >= v.nbClusters {
			return ErrClusterChainLoop
		}

		if err := v.readClusterInto(cluster, buf); err != nil {
			return err
		}
		if err := visitor(cluster, buf); err != nil {
			return err
		}

		nextCluster, err := v.nextCluster(cluster)
		if err != nil {
			return err
		}
		visited++
		if nextCluster >= EXFAT_EOF_START && nextCluster <= EXFAT_EOF_END {
			return nil
		}
		cluster = nextCluster
	}
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

	if entry.noFatChain {
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

func (v *VBR) extractEntryContent(entry Entry, dstpath string) error {
	dstfile, err := os.Create(dstpath)
	if err != nil {
		return err
	}
	defer dstfile.Close()

	// A zero-length entry yields an empty file. Returning early also keeps
	// entries with no allocation ($OrphanFiles, empty files, directories with a
	// zero data length) away from the cluster arithmetic below, where an
	// entryCluster of 0 would underflow into a nonsense offset.
	if entry.dataLen == 0 {
		return nil
	}

	if entry.isRegion {
		return v.extractRegion(entry, dstfile)
	}

	if !entry.noFatChain {
		return v.extractFatChainedContent(entry, dstfile)
	}

	return v.extractContiguesContent(entry, dstfile)
}

// extractRegion copies a fixed byte range of the image, which is how the
// synthetic $MBR, $FAT1 and $FAT2 entries are backed.
func (v *VBR) extractRegion(entry Entry, dstfile *os.File) error {
	length, err := safeInt64(entry.dataLen)
	if err != nil {
		return err
	}
	offset, err := safeInt64(entry.regionOffset)
	if err != nil {
		return err
	}

	section, err := v.sectionReader(offset, length)
	if err != nil {
		return err
	}

	_, err = io.CopyN(dstfile, section, length)
	return err
}

func (v *VBR) extractContiguesContent(entry Entry, dstfile *os.File) error {
	if !v.isValidCluster(entry.entryCluster) {
		return fmt.Errorf("%w: %d", ErrInvalidCluster, entry.entryCluster)
	}

	length, err := safeInt64(entry.dataLen)
	if err != nil {
		return err
	}
	offset, err := safeInt64(v.getClusterOffset(entry.entryCluster))
	if err != nil {
		return err
	}

	section, err := v.sectionReader(offset, length)
	if err != nil {
		return err
	}

	_, err = io.CopyN(dstfile, section, length)
	return err
}

func (v *VBR) extractFatChainedContent(entry Entry, dstfile *os.File) error {
	return v.visitEntryData(entry, func(_ uint32, chunk []byte) error {
		_, err := dstfile.Write(chunk)
		return err
	})
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
	clusterList := getRange(entry.entryCluster, sizeInClusters)

	var err error
	if !entry.noFatChain {
		clusterList, err = v.getChainedClusterList(entry.entryCluster)
		if err != nil {
			return nil, 0, err
		}
	}

	latestCluster := clusterList[len(clusterList)-1]
	_, err = v.readClusters(latestCluster, 1)
	if err != nil {
		return nil, 0, err
	}

	filetail := v.clusterSize
	if remainder > 0 {
		filetail = uint64(remainder)
	}

	return clusterList, filetail, nil
}

func (v *VBR) getChainedClusterList(cluster uint32) ([]uint32, error) {
	var clusterList []uint32
	err := v.visitFatChain(cluster, func(cluster uint32, _ []byte) error {
		clusterList = append(clusterList, cluster)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return clusterList, nil
}

func (v *VBR) countChainedClusters(cluster uint32) (int, error) {
	count := 0
	err := v.visitFatChain(cluster, func(uint32, []byte) error {
		count++
		return nil
	})
	if err != nil {
		return -1, err
	}
	return count, nil
}

func (v *VBR) countClusters(entry Entry) (int, error) {
	// Region entries ($MBR, $FAT1, $FAT2) are byte ranges outside the cluster
	// heap, so they have no clusters to count. Dividing their size by the
	// cluster size produces a number that looks like an answer and refers to
	// nothing - and contradicts getClusterList, which refuses them outright.
	// Locate them with Entry.GetRegionOffset and GetSize instead.
	if entry.isRegion {
		return 0, ErrNoClusterMapping
	}

	if entry.dataLen == 0 {
		return 0, nil
	}
	if entry.noFatChain {
		sizeInClusters, _ := v.size2Clusters(entry.dataLen)
		return int(sizeInClusters), nil
	}
	return v.countChainedClusters(entry.entryCluster)
}
