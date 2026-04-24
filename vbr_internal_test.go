package libxfat

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestParseVBRDataRejectsInvalidSectorSize(t *testing.T) {
	vbrData := validTestVBRBytes()
	vbrData[EXFAT_SECTOR_SIZE_OFFSET] = 8

	var vbr VBR
	err := vbr.parseVBRData(vbrData, 0, false)
	if err == nil || !strings.Contains(err.Error(), "invalid sector size") {
		t.Fatalf("parseVBRData() error = %v, want invalid sector size", err)
	}
}

func TestParseVBRDataRejectsZeroClusterCount(t *testing.T) {
	vbrData := validTestVBRBytes()
	binary.LittleEndian.PutUint32(vbrData[EXFAT_NB_CLUSTERS:EXFAT_NB_CLUSTERS+4], 0)

	var vbr VBR
	err := vbr.parseVBRData(vbrData, 0, false)
	if err == nil || !strings.Contains(err.Error(), "invalid cluster count") {
		t.Fatalf("parseVBRData() error = %v, want invalid cluster count", err)
	}
}

func TestParseVBRDataRejectsInvalidRootCluster(t *testing.T) {
	vbrData := validTestVBRBytes()
	binary.LittleEndian.PutUint32(vbrData[EXFAT_ROOT_CLUSTER_OFFSET:EXFAT_ROOT_CLUSTER_OFFSET+4], 1)

	var vbr VBR
	err := vbr.parseVBRData(vbrData, 0, false)
	if err == nil || !strings.Contains(err.Error(), "invalid root directory cluster") {
		t.Fatalf("parseVBRData() error = %v, want invalid root directory cluster", err)
	}
}

func validTestVBRBytes() []byte {
	vbrData := make([]byte, VBR_SIZE*int(SECTOR_SIZE))
	copy(vbrData[EXFAT_SIGN_OFFSET:EXFAT_SIGN_OFFSET+8], []byte(EXFAT_SIGNATURE))
	binary.LittleEndian.PutUint64(vbrData[EXFAT_VBR1_OFFSET:EXFAT_VBR1_OFFSET+8], 0)
	binary.LittleEndian.PutUint64(vbrData[EXFAT_VOLSIZE_OFFSET:EXFAT_VOLSIZE_OFFSET+8], 17)
	binary.LittleEndian.PutUint32(vbrData[EXFAT_FAT1_OFFSET:EXFAT_FAT1_OFFSET+4], 12)
	binary.LittleEndian.PutUint32(vbrData[EXFAT_FATSIZE_OFFSET:EXFAT_FATSIZE_OFFSET+4], 1)
	binary.LittleEndian.PutUint32(vbrData[EXFAT_DATA_OFFSET:EXFAT_DATA_OFFSET+4], 13)
	binary.LittleEndian.PutUint32(vbrData[EXFAT_NB_CLUSTERS:EXFAT_NB_CLUSTERS+4], 4)
	binary.LittleEndian.PutUint32(vbrData[EXFAT_ROOT_CLUSTER_OFFSET:EXFAT_ROOT_CLUSTER_OFFSET+4], 2)
	binary.LittleEndian.PutUint16(vbrData[EXFAT_VERSION_OFFSET:EXFAT_VERSION_OFFSET+2], 0x0100)
	vbrData[EXFAT_SECTOR_SIZE_OFFSET] = 9
	vbrData[EXFAT_CLUSTER_SIZE_OFFSET] = 0
	vbrData[EXFAT_PERCENT_USE_OFFSET] = 25
	binary.BigEndian.PutUint16(vbrData[SYNC_OFFSET:SYNC_OFFSET+2], SYNC_VALUE)
	return vbrData
}
