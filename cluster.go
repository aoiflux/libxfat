package libxfat

import (
	"errors"
	"fmt"
	"io"
	"os"
)

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
	v.dimage.Seek(int64(offset), io.SeekStart)

	if nbcluster > uint64(v.nbClusters) {
		errstring := fmt.Sprintf("out of range: %d", nbcluster)
		return nil, errors.New(errstring)
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
	var clusterdata []byte
	for cluster != FINAL_CLUSTER {
		data, err := v.readClusters(cluster, 1)
		if err != nil {
			return err
		}
		cluster = nextCluster
		visited++
	}

	return nil
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

func (v *VBR) readClustersFat(cluster uint32) ([]byte, error) {
	var clusterdata []byte
	err := v.visitFatChain(cluster, func(_ uint32, data []byte) error {
		clusterdata = append(clusterdata, data...)

		cluster, err = v.nextCluster(cluster)
		if err != nil {
			return nil, err
		}
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
	return data[:offset], nil
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
	return v.visitEntryData(entry, func(_ uint32, chunk []byte) error {
		_, err := dstfile.Write(chunk)
		return err
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
	for cluster != FINAL_CLUSTER {
		clusterList = append(clusterList, cluster)
		nextCluster, err := v.nextCluster(cluster)
		if err != nil && nextCluster < FINAL_CLUSTER {
			return nil, err
		}
		cluster = nextCluster
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
	if entry.dataLen == 0 {
		return 0, nil
	}
	if entry.noFatChain {
		sizeInClusters, _ := v.size2Clusters(entry.dataLen)
		return int(sizeInClusters), nil
	}
	return v.countChainedClusters(entry.entryCluster)
}
