package libxfat

import (
	"errors"
	"fmt"
	"io"
	"os"
)

func isEOFCluster(cluster uint32) bool {
	cluster &= EXFAT_CLUSTER_MASK
	return cluster >= EXFAT_EOF_START && cluster <= EXFAT_EOF_END
}

func (v *VBR) isValidCluster(cluster uint32) bool {
	if cluster < uint32(FIRST_CLUSTER_NUMBER) {
		return false
	}
	return uint64(cluster) <= uint64(v.nbClusters)+FIRST_CLUSTER_NUMBER-1
}

func (v *VBR) getClusterOffset(cluster uint32) uint64 {
	clusterNumber := uint64(cluster) - FIRST_CLUSTER_NUMBER
	offset := v.dataAreaStart + clusterNumber*v.clusterSize
	return offset
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

	offset := v.getClusterOffset(cluster)
	_, err := v.dimage.Seek(int64(offset), io.SeekStart)
	if err != nil {
		return nil, err
	}

	clusterdata := make([]byte, v.clusterSize*nbcluster)
	_, err = io.ReadFull(v.dimage, clusterdata)

	return clusterdata, err
}

func (v *VBR) nextCluster(cluster uint32) (uint32, error) {
	if !v.isValidCluster(cluster) {
		return 0, fmt.Errorf("%w: %d", ErrInvalidCluster, cluster)
	}

	fatEntries := (uint64(v.fatSize) * uint64(v.sectorSize)) / 4
	if uint64(cluster) >= fatEntries {
		errstring := fmt.Sprintf("cluster out of fat: %d", cluster)
		return 0, errors.New(errstring)
	}

	offset := int64(v.firstFat) + (int64(cluster) * 4)
	_, err := v.dimage.Seek(offset, io.SeekStart)
	if err != nil {
		return 0, err
	}

	data := make([]byte, 4)
	_, err = io.ReadFull(v.dimage, data)
	if err != nil {
		return 0, err
	}

	nextCluster := unpackLELong(data) & EXFAT_CLUSTER_MASK
	return nextCluster, nil
}

func (v *VBR) readClustersFat(cluster uint32) ([]byte, error) {
	chain, err := v.getChainedClusterList(cluster)
	if err != nil {
		return nil, err
	}
	if len(chain) == 0 {
		return []byte{}, nil
	}

	var clusterdata []byte
	for _, cluster := range chain {
		data, err := v.readClusters(cluster, 1)
		if err != nil {
			return nil, err
		}
		clusterdata = append(clusterdata, data...)
	}
	return clusterdata, nil
}

func (v *VBR) readClustersNoFat(sizeInClusters uint64, cluster uint32) ([]byte, error) {
	return v.readClusters(cluster, sizeInClusters)
}

func (v *VBR) size2Clusters(size uint64) (uint64, uint32) {
	sizeInClusters := size / v.clusterSize
	remainder := size % v.clusterSize
	if remainder > 0 {
		sizeInClusters++
	}
	return sizeInClusters, uint32(remainder)
}

func (v *VBR) readContent(entry Entry) ([]byte, error) {
	if entry.dataLen == 0 {
		return []byte{}, nil
	}

	if entry.noFatChain {
		sizeInClusters, _ := v.size2Clusters(entry.dataLen)
		data, err := v.readClustersNoFat(sizeInClusters, entry.entryCluster)
		if err != nil {
			return nil, err
		}
		return data[:entry.dataLen], nil
	}

	data, err := v.readClustersFat(entry.entryCluster)
	if err != nil {
		return nil, err
	}
	return data[:entry.dataLen], nil
}

func (v *VBR) extractEntryContent(entry Entry, dstpath string) error {
	dstfile, err := os.Create(dstpath)
	if err != nil {
		return err
	}
	defer dstfile.Close()

	if !entry.noFatChain {
		return v.extractFatChainedContent(entry, dstfile)
	}

	return v.extractContiguesContent(entry, dstfile)
}

func (v *VBR) extractContiguesContent(entry Entry, dstfile *os.File) error {
	entryClusterOffset := v.getClusterOffset(entry.entryCluster)
	_, err := v.dimage.Seek(int64(entryClusterOffset), io.SeekStart)
	if err != nil {
		return err
	}
	_, err = io.CopyN(dstfile, v.dimage, int64(entry.dataLen))
	return err
}

func (v *VBR) extractFatChainedContent(entry Entry, dstfile *os.File) error {
	clusterList, filetail, err := v.getClusterList(entry)
	if err != nil {
		return err
	}
	if len(clusterList) == 0 {
		return nil
	}

	allButLatestClusters := clusterList[:len(clusterList)-1]
	for _, cluster := range allButLatestClusters {
		data, err := v.readClusters(cluster, 1)
		if err != nil {
			return err
		}
		_, err = dstfile.Write(data)
		if err != nil {
			return err
		}
	}

	latestCluster := clusterList[len(clusterList)-1]
	data, err := v.readClusters(latestCluster, 1)
	if err != nil {
		return err
	}

	_, err = dstfile.Write(data[:filetail])
	return err
}

func (v *VBR) getClusterList(entry Entry) ([]uint32, uint64, error) {
	if entry.dataLen == 0 {
		return nil, 0, nil
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
	seen := make(map[uint32]struct{})
	for !isEOFCluster(cluster) {
		if !v.isValidCluster(cluster) {
			return nil, fmt.Errorf("%w: %d", ErrInvalidCluster, cluster)
		}
		if _, ok := seen[cluster]; ok {
			return nil, fmt.Errorf("%w at cluster %d", ErrClusterChainLoop, cluster)
		}
		seen[cluster] = struct{}{}
		clusterList = append(clusterList, cluster)
		if len(clusterList) > int(v.nbClusters) {
			return nil, fmt.Errorf("%w: chain length %d exceeds cluster count %d", ErrClusterChainLoop, len(clusterList), v.nbClusters)
		}

		nextCluster, err := v.nextCluster(cluster)
		if err != nil {
			return nil, err
		}
		if nextCluster == EXFAT_BAD_CLUSTER {
			return nil, fmt.Errorf("%w at cluster %d", ErrBadCluster, cluster)
		}
		if !isEOFCluster(nextCluster) && !v.isValidCluster(nextCluster) {
			return nil, fmt.Errorf("%w: %d", ErrInvalidCluster, nextCluster)
		}
		cluster = nextCluster
	}
	return clusterList, nil
}

func (v *VBR) countChainedClusters(cluster uint32) (int, error) {
	clusterList, err := v.getChainedClusterList(cluster)
	if err != nil {
		return -1, err
	}
	return len(clusterList), nil
}

func (v *VBR) countClusters(entry Entry) (int, error) {
	if entry.dataLen == 0 {
		return 0, nil
	}
	if entry.noFatChain {
		sizeInClusters, _ := v.size2Clusters(entry.dataLen)
		return int(sizeInClusters), nil
	}
	return v.countChainedClusters(entry.entryCluster)
}
