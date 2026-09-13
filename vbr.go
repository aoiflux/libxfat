package libxfat

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
)

func parseVBR(src Source) (VBR, error) {
	var vbr VBR
	vbr.visitPool = &sync.Pool{}
	vbr.dimage = src.Reader
	vbr.base = src.Base
	vbr.size = src.Size

	data := make([]byte, VBR_SIZE*SECTOR_SIZE)
	if err := vbr.readAt(data, src.Base); err != nil {
		return vbr, err
	}

	if err := vbr.parseVBRData(data, src); err != nil {
		return vbr, err
	}

	return vbr, nil
}

func (v *VBR) parseVBRData(vbr []byte, src Source) error {
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

	v.base = src.Base
	// vbrOffset is what the volume claims about itself, which is not necessarily
	// where we found it. Reconciling the two is checkPartitionOffset's job.
	v.vbrOffset = unpackLELongLong(vbr[EXFAT_VBR1_OFFSET : EXFAT_VBR1_OFFSET+8])
	v.volumeSize = unpackLELongLong(vbr[EXFAT_VOLSIZE_OFFSET : EXFAT_VOLSIZE_OFFSET+8])
	v.fatOffset = unpackLELong(vbr[EXFAT_FAT1_OFFSET : EXFAT_FAT1_OFFSET+4])
	v.fatSize = unpackLELong(vbr[EXFAT_FATSIZE_OFFSET : EXFAT_FATSIZE_OFFSET+4])
	v.dataRegionOffset = unpackLELong(vbr[EXFAT_DATA_OFFSET : EXFAT_DATA_OFFSET+4])
	v.nbClusters = unpackLELong(vbr[EXFAT_NB_CLUSTERS : EXFAT_NB_CLUSTERS+4])
	v.rootDirCluster = unpackLELong(vbr[EXFAT_ROOT_CLUSTER_OFFSET : EXFAT_ROOT_CLUSTER_OFFSET+4])
	v.serialNumber = unpackLELong(vbr[EXFAT_SN_OFFSET : EXFAT_SN_OFFSET+4])
	v.version = unpackLEShort(vbr[EXFAT_VERSION_OFFSET : EXFAT_VERSION_OFFSET+2])
	v.volumeFlags = unpackLEShort(vbr[EXFAT_VOLUME_FLAGS_OFFSET : EXFAT_VOLUME_FLAGS_OFFSET+2])
	v.sectorSize = 1 << vbr[EXFAT_SECTOR_SIZE_OFFSET]
	v.sectorsPerCluster = 1 << vbr[EXFAT_CLUSTER_SIZE_OFFSET]
	v.numberOfFats = vbr[EXFAT_NUMBER_OF_FATS_OFFSET]
	v.clusterSize = uint64(v.sectorSize) * uint64(v.sectorsPerCluster)
	// Every absolute offset hangs off base, which is where the volume boot
	// record was actually read from. Deriving it from the recorded
	// PartitionOffset instead - as releases before v1.1.0 did - silently
	// mislocates the data region on any volume whose sector size is not 512.
	v.firstFat = uint64(v.base) + uint64(v.fatOffset)*uint64(v.sectorSize)
	v.dataAreaStart = uint64(v.base) + uint64(v.dataRegionOffset)*uint64(v.sectorSize)
	v.percentInUse = vbr[EXFAT_PERCENT_USE_OFFSET]

	// Layout validation first: it sanity-checks sectorSize, which the
	// PartitionOffset comparison depends on.
	err = v.validateLayout()
	if err != nil {
		return err
	}

	if src.Strict {
		if err := v.checkPartitionOffset(src); err != nil {
			return err
		}
	}

	return nil
}

// checkPartitionOffset reconciles the PartitionOffset recorded in the volume
// boot record with where the caller actually opened the volume.
//
// When the caller knows the partition's LBA it is compared directly. Otherwise
// the expectation is derived from the base offset, which reproduces the
// historical check for whole-disk images while doing the comparison in bytes so
// that it stays correct when the sector size is not 512.
func (v *VBR) checkPartitionOffset(src Source) error {
	if src.IgnorePartitionOffset {
		return nil
	}

	claimed := v.vbrOffset

	if src.PartitionLBA != 0 {
		if claimed != src.PartitionLBA {
			return fmt.Errorf("%w: volume records partition offset %d, expected LBA %d",
				ErrPartitionOffsetMismatch, claimed, src.PartitionLBA)
		}
		return nil
	}

	claimedBytes, err := safeInt64(claimed * uint64(v.sectorSize))
	if err != nil || claimed != 0 && claimedBytes/int64(v.sectorSize) != int64(claimed) {
		return fmt.Errorf("%w: volume records an unrepresentable partition offset %d",
			ErrPartitionOffsetMismatch, claimed)
	}
	if claimedBytes != v.base {
		return fmt.Errorf("%w: volume records partition offset %d (byte %d) but was opened at byte %d; "+
			"pass Source.PartitionLBA or Source.IgnorePartitionOffset when reading a partition-relative image",
			ErrPartitionOffsetMismatch, claimed, claimedBytes, v.base)
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

func checkExfatSignature(signature string) error {
	if signature != EXFAT_SIGNATURE {
		return ErrExfatSignature
	}
	return nil
}
func checkSyncValue(packedBytes []byte) error {
	unpackedValue := unpackBEShort(packedBytes)
	if unpackedValue != SYNC_VALUE {
		return ErrSyncValue
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
