package libxfat

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

func parseVBR(dimage *os.File, offset uint64, optmistic bool) (VBR, error) {
	var vbr VBR

	seekByte := int64(offset) * int64(SECTOR_SIZE)
	_, err := dimage.Seek(seekByte, io.SeekStart)
	if err != nil {
		return vbr, err
	}

	data := make([]byte, VBR_SIZE*SECTOR_SIZE)
	_, err = io.ReadFull(dimage, data)
	if err != nil {
		return vbr, err
	}

	vbr.dimage = dimage
	err = vbr.parseVBRData(data, offset, optmistic)
	if err != nil {
		return vbr, err
	}

	return vbr, nil
}

func (v *VBR) parseVBRData(vbr []byte, offset uint64, optimistic bool) error {
	err := checkSyncValue(vbr[SYNC_OFFSET : SYNC_OFFSET+2])
	if err != nil {
		return err
	}

	signature := string(vbr[EXFAT_SIGN_OFFSET : EXFAT_SIGN_OFFSET+8])
	err = checkExfatSignature(signature)
	if err != nil {
		return err
	}
	v.signature = signature

	if optimistic {
		v.vbrOffset = offset
	} else {
		vbrOffset, err := checkVbrOffset(vbr[EXFAT_VBR1_OFFSET:EXFAT_VBR1_OFFSET+8], offset)
		if err != nil {
			return err
		}
		v.vbrOffset = vbrOffset
	}

	v.volumeSize = unpackLELongLong(vbr[EXFAT_VOLSIZE_OFFSET : EXFAT_VOLSIZE_OFFSET+8])
	v.fatOffset = unpackLELong(vbr[EXFAT_FAT1_OFFSET : EXFAT_FAT1_OFFSET+4])
	v.fatSize = unpackLELong(vbr[EXFAT_FATSIZE_OFFSET : EXFAT_FATSIZE_OFFSET+4])
	v.dataRegionOffset = unpackLELong(vbr[EXFAT_DATA_OFFSET : EXFAT_DATA_OFFSET+4])
	v.nbClusters = unpackLELong(vbr[EXFAT_NB_CLUSTERS : EXFAT_NB_CLUSTERS+4])
	v.rootDirCluster = unpackLELong(vbr[EXFAT_ROOT_CLUSTER_OFFSET : EXFAT_ROOT_CLUSTER_OFFSET+4])
	v.sn = vbr[EXFAT_SN_OFFSET : EXFAT_SN_OFFSET+4]
	v.version = unpackLEShort(vbr[EXFAT_VERSION_OFFSET : EXFAT_VERSION_OFFSET+2])
	v.sectorSize = 1 << vbr[EXFAT_SECTOR_SIZE_OFFSET]
	v.sectorsPerCluster = 1 << vbr[EXFAT_CLUSTER_SIZE_OFFSET]
	v.clusterSize = uint64(v.sectorSize) * uint64(v.sectorsPerCluster)
	v.vbrStart = v.vbrOffset * uint64(v.sectorSize)
	v.firstFat = uint64(v.fatOffset)*uint64(v.sectorSize) + v.vbrStart
	v.dataAreaStart = v.vbrStart + uint64(v.dataRegionOffset)*uint64(v.sectorSize)
	v.percentInUse = vbr[EXFAT_PERCENT_USE_OFFSET]

	err = v.validateLayout()
	if err != nil {
		return err
	}

	return nil
}

func (v VBR) validateLayout() error {
	sectorShift := uint32(0)
	for (uint32(1) << sectorShift) < v.sectorSize {
		sectorShift++
	}

	if v.sectorSize < 512 || v.sectorSize > 4096 || (uint32(1)<<sectorShift) != v.sectorSize {
		return fmt.Errorf("invalid sector size: %d", v.sectorSize)
	}
	if sectorShift < 9 || sectorShift > 12 {
		return fmt.Errorf("invalid sector size shift: %d", sectorShift)
	}
	if v.sectorsPerCluster == 0 {
		return errors.New("invalid sectors per cluster")
	}
	clusterShift := uint32(0)
	for (uint32(1) << clusterShift) < v.sectorsPerCluster {
		clusterShift++
	}
	if (uint32(1) << clusterShift) != v.sectorsPerCluster {
		return fmt.Errorf("invalid sectors per cluster: %d", v.sectorsPerCluster)
	}
	if sectorShift+clusterShift > 25 {
		return fmt.Errorf("invalid cluster size: sectorShift=%d clusterShift=%d", sectorShift, clusterShift)
	}
	if v.volumeSize == 0 {
		return errors.New("invalid volume size")
	}
	if v.fatOffset == 0 || uint64(v.fatOffset) >= v.volumeSize {
		return fmt.Errorf("invalid FAT offset: %d", v.fatOffset)
	}
	if v.fatSize == 0 {
		return errors.New("invalid FAT size")
	}
	if v.dataRegionOffset <= v.fatOffset+v.fatSize-1 || uint64(v.dataRegionOffset) >= v.volumeSize {
		return fmt.Errorf("invalid data region offset: %d", v.dataRegionOffset)
	}
	if v.nbClusters == 0 {
		return errors.New("invalid cluster count")
	}
	clusterHeapSectors := uint64(v.nbClusters) * uint64(v.sectorsPerCluster)
	if uint64(v.dataRegionOffset)+clusterHeapSectors > v.volumeSize {
		return fmt.Errorf("cluster heap exceeds volume: dataOffset=%d clusterHeap=%d volume=%d", v.dataRegionOffset, clusterHeapSectors, v.volumeSize)
	}
	if v.rootDirCluster < uint32(FIRST_CLUSTER_NUMBER) || uint64(v.rootDirCluster) > uint64(v.nbClusters)+FIRST_CLUSTER_NUMBER-1 {
		return fmt.Errorf("invalid root directory cluster: %d", v.rootDirCluster)
	}

	return nil
}

func checkVbrOffset(packedBytes []byte, offset uint64) (uint64, error) {
	unpackedValue := unpackLELongLong(packedBytes)
	if offset != unpackedValue {
		return 0, errors.New("invalid vbr address")
	}
	return unpackedValue, nil
}
func checkExfatSignature(signature string) error {
	if signature != EXFAT_SIGNATURE {
		return errors.New("exfat signature mismatch")
	}
	return nil
}
func checkSyncValue(packedBytes []byte) error {
	unpackedValue := unpackBEShort(packedBytes)
	if unpackedValue != SYNC_VALUE {
		return errors.New("no sync value in vbr")
	}
	return nil
}

// unpackBEShort is python's >H unpack equivalent
func unpackBEShort(packedBytes []byte) uint16 {
	return binary.BigEndian.Uint16(packedBytes)
}

// unpaclLEShort is python's <H unpack equivalent
func unpackLEShort(packedBytes []byte) uint16 {
	return binary.LittleEndian.Uint16(packedBytes)
}

// unpackLELong is python's <L unpack equivalent
func unpackLELong(packedBytes []byte) uint32 {
	return binary.LittleEndian.Uint32(packedBytes)
}

// unpackLELongLong is python's <Q unpack equivalent
func unpackLELongLong(packedBytes []byte) uint64 {
	return binary.LittleEndian.Uint64(packedBytes)
}
