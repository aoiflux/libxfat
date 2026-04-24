package libxfat

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCountBitmapHandlesTailBytes(t *testing.T) {
	bitmap := []byte{0b00000111, 0b00000011, 0b00000001}

	if got := countBitmap(bitmap); got != 6 {
		t.Fatalf("countBitmap() = %d, want 6", got)
	}
}

func TestGetChainedClusterListAcceptsEOFRange(t *testing.T) {
	vbr := newTestVBRWithFAT(t, map[uint32]uint32{
		2: EXFAT_EOF_START,
	})

	chain, err := vbr.getChainedClusterList(2)
	if err != nil {
		t.Fatalf("getChainedClusterList() error = %v", err)
	}
	if len(chain) != 1 || chain[0] != 2 {
		t.Fatalf("getChainedClusterList() = %v, want [2]", chain)
	}
}

func TestGetChainedClusterListDetectsLoop(t *testing.T) {
	vbr := newTestVBRWithFAT(t, map[uint32]uint32{
		2: 3,
		3: 2,
	})

	_, err := vbr.getChainedClusterList(2)
	if !errors.Is(err, ErrClusterChainLoop) {
		t.Fatalf("getChainedClusterList() error = %v, want ErrClusterChainLoop", err)
	}
}

func TestGetClusterListZeroLength(t *testing.T) {
	vbr := VBR{clusterSize: 512}
	entry := Entry{dataLen: 0, noFatChain: true}

	clusters, tail, err := vbr.getClusterList(entry)
	if err != nil {
		t.Fatalf("getClusterList() error = %v", err)
	}
	if len(clusters) != 0 {
		t.Fatalf("getClusterList() clusters = %v, want empty", clusters)
	}
	if tail != 0 {
		t.Fatalf("getClusterList() tail = %d, want 0", tail)
	}
}

func newTestVBRWithFAT(t *testing.T, entries map[uint32]uint32) VBR {
	t.Helper()

	imagePath := filepath.Join(t.TempDir(), "fat.bin")
	image, err := os.Create(imagePath)
	if err != nil {
		t.Fatalf("create FAT image: %v", err)
	}
	t.Cleanup(func() {
		_ = image.Close()
	})

	data := make([]byte, 512)
	for cluster, next := range entries {
		binary.LittleEndian.PutUint32(data[int(cluster)*4:int(cluster+1)*4], next)
	}
	if _, err := image.Write(data); err != nil {
		t.Fatalf("write FAT image: %v", err)
	}
	if _, err := image.Seek(0, 0); err != nil {
		t.Fatalf("rewind FAT image: %v", err)
	}

	return VBR{
		dimage:     image,
		fatSize:    1,
		sectorSize: 512,
		nbClusters: 8,
	}
}
