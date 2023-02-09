package libxfat

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
)

func parseVBR(dimage *os.File, offset uint64, optmistic bool) (VBR, error) {
	var vbr VBR

	seekByte := int64(offset) * int64(SECTOR_SIZE)
	dimage.Seek(seekByte, io.SeekStart)

	data := make([]byte, VBR_SIZE*SECTOR_SIZE)
	_, err := dimage.Read(data)
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
