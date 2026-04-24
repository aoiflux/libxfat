package test

import (
	"testing"

	"github.com/aoiflux/libxfat"
)

func TestReadRootDirIncludesVirtuals(t *testing.T) {
	image := createTestImage(t)
	exfat, err := libxfat.New(image, false)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	entries, err := exfat.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir error: %v", err)
	}

	found := map[string]bool{}
	for _, v := range entries {
		found[v.GetName()] = true
	}

	expected := []string{"$MBR", "$FAT1", "$OrphanFiles"}
	for _, name := range expected {
		if !found[name] {
			t.Errorf("expected virtual entry %s not found in root dir", name)
		}
	}
}
