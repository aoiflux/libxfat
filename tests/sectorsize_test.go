package test

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoiflux/libxfat/v2"
)

// A 4Kn volume: 4096-byte sectors, one sector per cluster.
const (
	fourKSectorSize   = 4096
	fourKFatOffset    = 2
	fourKFatSize      = 1
	fourKDataOffset   = 3
	fourKClusterCount = 4
	fourKVolumeSector = 7
	fourKRootCluster  = 2
	fourKBitmapClust  = 3
	fourKUpcaseClust  = 4

	// The volume sits one 4096-byte sector into the disk. Expressed in the
	// 512-byte units that New and NewFromReaderAt take, that is sector 8, while
	// the volume itself records LBA 1 in its own sector size. Reconciling those
	// two numbers is exactly what the byte-based PartitionOffset check does.
	fourKVolumeBase = 4096
	fourKVolumeLBA  = 1
	fourKOffset512  = fourKVolumeBase / 512

	fourKBitmapByte = 0x03
)

// buildFourKImage returns a disk holding one 4Kn exFAT volume at
// fourKVolumeBase, plus the byte offset at which cluster fourKBitmapClust lands.
func buildFourKImage() (disk []byte, bitmapOffset int64) {
	volume := make([]byte, fourKVolumeSector*fourKSectorSize)

	copy(volume[3:11], []byte("EXFAT   "))
	binary.LittleEndian.PutUint64(volume[0x40:0x48], fourKVolumeLBA)
	binary.LittleEndian.PutUint64(volume[0x48:0x50], fourKVolumeSector)
	binary.LittleEndian.PutUint32(volume[0x50:0x54], fourKFatOffset)
	binary.LittleEndian.PutUint32(volume[0x54:0x58], fourKFatSize)
	binary.LittleEndian.PutUint32(volume[0x58:0x5c], fourKDataOffset)
	binary.LittleEndian.PutUint32(volume[0x5c:0x60], fourKClusterCount)
	binary.LittleEndian.PutUint32(volume[0x60:0x64], fourKRootCluster)
	binary.LittleEndian.PutUint16(volume[0x68:0x6a], 0x0100)
	volume[0x6c] = 12 // 1 << 12 == 4096-byte sectors
	volume[0x6d] = 0  // one sector per cluster
	volume[0x70] = 25
	binary.BigEndian.PutUint16(volume[0x1fe:0x200], 0x55aa)

	// FAT: every used cluster terminates its own chain.
	fat := volume[fourKFatOffset*fourKSectorSize:]
	for _, cluster := range []int{fourKRootCluster, fourKBitmapClust, fourKUpcaseClust} {
		binary.LittleEndian.PutUint32(fat[cluster*4:cluster*4+4], 0xffffffff)
	}

	// Root directory in cluster 2: an allocation bitmap and an up-case table.
	root := volume[fourKDataOffset*fourKSectorSize:]
	root[0] = 0x81
	binary.LittleEndian.PutUint32(root[20:24], fourKBitmapClust)
	binary.LittleEndian.PutUint64(root[24:32], 1)

	root[32] = 0x82
	binary.LittleEndian.PutUint32(root[52:56], fourKUpcaseClust)
	binary.LittleEndian.PutUint64(root[56:64], 64)

	// Bitmap content lives in cluster 3, one cluster past the root.
	bitmapInVolume := (fourKDataOffset + (fourKBitmapClust - fourKRootCluster)) * fourKSectorSize
	volume[bitmapInVolume] = fourKBitmapByte

	disk = make([]byte, fourKVolumeBase+len(volume))
	copy(disk[fourKVolumeBase:], volume)

	return disk, int64(fourKVolumeBase + bitmapInVolume)
}

// TestFourKSectorDataRegionOffset locks the sector-size fix. Deriving the
// volume's byte position from the recorded PartitionOffset multiplied by the
// real sector size - as releases before v1.1.0 did - mislocates the data region
// on any volume whose sector size is not 512, because the VBR itself is read at
// a 512-byte-unit offset. The two must agree, and only base does.
func TestFourKSectorDataRegionOffset(t *testing.T) {
	disk, wantBitmapOffset := buildFourKImage()

	fs, err := libxfat.NewFromReaderAt(bytes.NewReader(disk), int64(len(disk)), false, fourKOffset512)
	if err != nil {
		t.Fatalf("NewFromReaderAt() on a 4Kn volume error = %v", err)
	}

	if got := fs.ClusterSize(); got != fourKSectorSize {
		t.Fatalf("ClusterSize() = %d, want %d", got, fourKSectorSize)
	}

	if got := mustClusterOffset(t, fs, fourKBitmapClust); got != uint64(wantBitmapOffset) {
		t.Fatalf("ClusterOffset(%d) = %d, want %d",
			fourKBitmapClust, got, wantBitmapOffset)
	}

	entries, err := fs.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir() error = %v", err)
	}

	// Reading the bitmap through the cluster machinery proves the data region
	// was located correctly, not merely computed consistently.
	bitmap := findEntry(t, entries, "$BitMap")
	dst := filepath.Join(t.TempDir(), "bitmap.bin")
	if err := fs.ExtractEntryContent(bitmap, dst); err != nil {
		t.Fatalf("ExtractEntryContent($BitMap) error = %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if want := []byte{fourKBitmapByte}; !bytes.Equal(got, want) {
		t.Fatalf("extracted $BitMap = %v, want %v", got, want)
	}
}

// TestFourKSectorBootRegionSize checks that the synthetic $MBR entry spans 12
// sectors of the volume's own sector size rather than a hardcoded 512.
func TestFourKSectorBootRegionSize(t *testing.T) {
	disk, _ := buildFourKImage()

	fs, err := libxfat.NewFromReaderAt(bytes.NewReader(disk), int64(len(disk)), false, fourKOffset512)
	if err != nil {
		t.Fatalf("NewFromReaderAt() error = %v", err)
	}
	entries, err := fs.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir() error = %v", err)
	}

	mbr := findEntry(t, entries, "$MBR")
	if want := uint64(12 * fourKSectorSize); mbr.Size() != want {
		t.Fatalf("$MBR size = %d, want %d", mbr.Size(), want)
	}
}

// TestFourKSectorPartitionLBA covers the same volume opened through a
// partition-scoped reader, where the recorded LBA is in 4096-byte sectors.
func TestFourKSectorPartitionLBA(t *testing.T) {
	disk, _ := buildFourKImage()
	volume := disk[fourKVolumeBase:]

	fs, err := libxfat.Open(libxfat.Source{
		Reader:       bytes.NewReader(volume),
		Size:         int64(len(volume)),
		Strict:       true,
		PartitionLBA: fourKVolumeLBA,
	})
	if err != nil {
		t.Fatalf("Open() with PartitionLBA error = %v", err)
	}

	if _, err := fs.ReadRootDir(); err != nil {
		t.Fatalf("ReadRootDir() error = %v", err)
	}

	// Offsets are relative to the reader, so the data region now starts at the
	// volume-relative position.
	wantCluster := uint64(fourKDataOffset+(fourKBitmapClust-fourKRootCluster)) * fourKSectorSize
	if got := mustClusterOffset(t, fs, fourKBitmapClust); got != wantCluster {
		t.Fatalf("ClusterOffset(%d) = %d, want %d", fourKBitmapClust, got, wantCluster)
	}
}

// mustClusterOffset is ClusterOffset with the error turned into a test failure, for
// the assertions below where an invalid cluster would mean the fixture is wrong
// rather than the library.
func mustClusterOffset(t *testing.T, fs *libxfat.ExFAT, cluster uint32) uint64 {
	t.Helper()

	offset, err := fs.ClusterOffset(cluster)
	if err != nil {
		t.Fatalf("ClusterOffset(%d): %v", cluster, err)
	}
	return offset
}
