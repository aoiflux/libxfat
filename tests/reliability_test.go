package test

import (
	"errors"
	"testing"

	"github.com/aoiflux/libxfat"
)

func TestReadRootDirAcceptsEOFClusterRange(t *testing.T) {
	image := createEOFRangeRootDirImage(t)
	exfat, err := libxfat.New(image, false)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	entries, err := exfat.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir error: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected root directory entries")
	}
}

func TestReadRootDirDetectsFATLoop(t *testing.T) {
	image := createLoopedRootDirImage(t)
	exfat, err := libxfat.New(image, false)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	_, err = exfat.ReadRootDir()
	if !errors.Is(err, libxfat.ErrClusterChainLoop) {
		t.Fatalf("expected ErrClusterChainLoop, got %v", err)
	}
}

func TestAllocatedAndFreeClustersFromShortBitmap(t *testing.T) {
	image := createTestImage(t)
	exfat, err := libxfat.New(image, false)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	if _, err := exfat.ReadRootDir(); err != nil {
		t.Fatalf("ReadRootDir error: %v", err)
	}

	allocated, err := exfat.GetAllocatedClusters()
	if err != nil {
		t.Fatalf("GetAllocatedClusters error: %v", err)
	}
	if allocated != 2 {
		t.Fatalf("allocated clusters = %d, want 2", allocated)
	}

	free, err := exfat.GetFreeClusters()
	if err != nil {
		t.Fatalf("GetFreeClusters error: %v", err)
	}
	if free != 2 {
		t.Fatalf("free clusters = %d, want 2", free)
	}
}
