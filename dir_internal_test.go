package libxfat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGetAllocatedClustersWithoutBitmapFails(t *testing.T) {
	exfat := ExFAT{}

	_, err := exfat.GetAllocatedClusters()
	if !errors.Is(err, ErrAllocationBitmapNotFound) {
		t.Fatalf("GetAllocatedClusters() error = %v, want ErrAllocationBitmapNotFound", err)
	}
}

func TestProcessEntryExtractPreservesRelativePath(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "content.bin")
	image, err := os.Create(imagePath)
	if err != nil {
		t.Fatalf("create image: %v", err)
	}
	t.Cleanup(func() {
		_ = image.Close()
	})

	if _, err := image.Write([]byte("payload!!")); err != nil {
		t.Fatalf("write image: %v", err)
	}
	if _, err := image.Seek(0, 0); err != nil {
		t.Fatalf("rewind image: %v", err)
	}

	exfat := ExFAT{
		vbr: VBR{
			dimage:        image,
			clusterSize:   8,
			nbClusters:    4,
			dataAreaStart: 0,
		},
	}
	entry := Entry{
		etype:        EXFAT_DIRRECORD_FILEDIR,
		name:         "child.txt",
		dataLen:      7,
		entryCluster: 2,
		noFatChain:   true,
	}

	outDir := t.TempDir()
	if err := exfat.processEntry(entry, "/nested/dir/", outDir, true, false, false); err != nil {
		t.Fatalf("processEntry() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "nested", "dir", "child.txt"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "payload" {
		t.Fatalf("extracted file contents = %q, want %q", string(data), "payload")
	}
}
