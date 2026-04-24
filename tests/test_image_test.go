package test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

const (
	testSectorSize      = 512
	testFatOffsetSector = 12
	testFatSizeSectors  = 1
	testDataOffset      = 13
	testClusterCount    = 4
	testRootCluster     = 2
	testBitmapCluster   = 3
	testUpcaseCluster   = 4
	testVolumeSectors   = 17
	testSyncOffset      = 0x1fe

	testBitmapEntryType = 0x81
	testUpcaseEntryType = 0x82
	testVolumeGUIDType  = 0xA0
	testTexFATType      = 0xA1
	testACTType         = 0xE2
	testFinalCluster    = 0xffffffff
)

func createTestImage(t *testing.T) *os.File {
	t.Helper()

	imagePath := filepath.Join(t.TempDir(), "minimal.exfat")
	image, err := os.Create(imagePath)
	if err != nil {
		t.Fatalf("create image: %v", err)
	}
	t.Cleanup(func() {
		_ = image.Close()
	})

	data := make([]byte, testVolumeSectors*testSectorSize)
	writeTestVBR(data)
	writeTestFAT(data[testFatOffsetSector*testSectorSize:])
	writeTestRootDir(data[testDataOffset*testSectorSize:])

	if _, err := image.Write(data); err != nil {
		t.Fatalf("write image: %v", err)
	}
	if _, err := image.Seek(0, 0); err != nil {
		t.Fatalf("rewind image: %v", err)
	}

	return image
}

func writeTestVBR(dst []byte) {
	copy(dst[3:11], []byte("EXFAT   "))
	binary.LittleEndian.PutUint64(dst[0x40:0x48], 0)
	binary.LittleEndian.PutUint64(dst[0x48:0x50], testVolumeSectors)
	binary.LittleEndian.PutUint32(dst[0x50:0x54], testFatOffsetSector)
	binary.LittleEndian.PutUint32(dst[0x54:0x58], testFatSizeSectors)
	binary.LittleEndian.PutUint32(dst[0x58:0x5c], testDataOffset)
	binary.LittleEndian.PutUint32(dst[0x5c:0x60], testClusterCount)
	binary.LittleEndian.PutUint32(dst[0x60:0x64], testRootCluster)
	binary.LittleEndian.PutUint16(dst[0x68:0x6a], 0x0100)
	dst[0x6c] = 9
	dst[0x6d] = 0
	dst[0x70] = 25
	binary.BigEndian.PutUint16(dst[testSyncOffset:testSyncOffset+2], 0x55aa)
}

func writeTestFAT(dst []byte) {
	binary.LittleEndian.PutUint32(dst[2*4:3*4], testFinalCluster)
	binary.LittleEndian.PutUint32(dst[3*4:4*4], testFinalCluster)
	binary.LittleEndian.PutUint32(dst[4*4:5*4], testFinalCluster)
}

func writeTestRootDir(dst []byte) {
	bitmap := dst[0:32]
	bitmap[0] = testBitmapEntryType
	binary.LittleEndian.PutUint32(bitmap[20:24], testBitmapCluster)
	binary.LittleEndian.PutUint64(bitmap[24:32], 1)

	upcase := dst[32:64]
	upcase[0] = testUpcaseEntryType
	binary.LittleEndian.PutUint32(upcase[20:24], testUpcaseCluster)
	binary.LittleEndian.PutUint64(upcase[24:32], 64)

	dst[64] = testVolumeGUIDType
	dst[96] = testTexFATType
	dst[128] = testACTType
	// Leave the next record zeroed to terminate directory parsing.
}
