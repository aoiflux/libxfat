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
	offset := v.getClusterOffset(cluster)
	v.dimage.Seek(int64(offset), io.SeekStart)

	if nbcluster > uint64(v.nbClusters) {
		errstring := fmt.Sprintf("out of range: %d", nbcluster)
		return nil, errors.New(errstring)
	}

	clusterdata := make([]byte, v.clusterSize*nbcluster)
	_, err := v.dimage.Read(clusterdata)

	return clusterdata, err
}

func (v *VBR) nextCluster(cluster uint32) (uint32, error) {
	fatTotal := uint64(v.fatSize) * uint64(v.sectorSize)
	if (uint64(cluster)*4) > fatTotal || cluster < 2 {
		errstring := fmt.Sprintf("cluster out of fat: %d", cluster)
		return 0, errors.New(errstring)
	}

	offset := int64(v.firstFat) + (int64(cluster) * 4)
	v.dimage.Seek(offset, io.SeekStart)

	data := make([]byte, 4)
	_, err := v.dimage.Read(data)
	if err != nil {
		return 0, err
	}

	return unpackLELong(data), nil
}

func (v *VBR) readClustersFat(cluster uint32) ([]byte, error) {
	var clusterdata []byte
	for cluster != FINAL_CLUSTER {
		data, err := v.readClusters(cluster, 1)
		if err != nil {
			return nil, err
		}
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
	return data[:entry.dataLen], nil
}

func (v *VBR) extractEntryContent(entry Entry, dstpath string) error {
	dstfile, err := os.Create(dstpath)
	if err != nil {
		return err
	}
	defer dstfile.Close()

	clusterList, filetail, err := v.getClusterList(entry)
	if err != nil {
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
	if err != nil {
		return err
	}

	return nil
}

func (v *VBR) getClusterList(entry Entry) ([]uint32, uint64, error) {
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

	if dataRemainder := entry.dataLen / v.clusterSize; dataRemainder > 0 {
		filetail = dataRemainder
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
	clusterList, err := v.getChainedClusterList(cluster)
	if err != nil {
		return -1, err
	}
	return len(clusterList), nil
}

func (v *VBR) countClusters(entry Entry) (int, error) {
	if entry.noFatChain {
		sizeInClusters, _ := v.size2Clusters(entry.dataLen)
		return int(sizeInClusters), nil
	}
	return v.countChainedClusters(entry.entryCluster)
}
