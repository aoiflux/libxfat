package test

import (
	"testing"

	"github.com/aoiflux/libxfat/v2"
)

func TestVirtualEntriesPresence(t *testing.T) {
	image := createTestImage(t)
	exfat, err := libxfat.New(image, false)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	virtuals, err := exfat.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir error: %v", err)
	}

	found := map[string]bool{}
	for _, entry := range virtuals {
		if entry.IsVirtualEntry() {
			found[entry.Name()] = true
		}
	}

	expected := []string{"$MBR", "$FAT1", "$OrphanFiles"}
	for _, name := range expected {
		if !found[name] {
			t.Errorf("expected virtual entry %s not found", name)
		}
	}
}

func TestVirtualEntryAttributes(t *testing.T) {
	image := createTestImage(t)
	exfat, err := libxfat.New(image, false)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	virtuals, err := exfat.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir error: %v", err)
	}

	for _, entry := range virtuals {
		if !entry.IsVirtualEntry() {
			continue
		}
		if entry.Name() == "$MBR" && entry.Size() != 12*512 {
			t.Errorf("$MBR size incorrect: got %d, want %d", entry.Size(), 12*512)
		}
		if entry.Name() == "$FAT1" && entry.Size() != 1*512 {
			t.Errorf("$FAT1 size incorrect: got %d, want %d", entry.Size(), 1*512)
		}
		if entry.Name() == "$OrphanFiles" && entry.Size() != 0 {
			t.Errorf("$OrphanFiles size should be 0")
		}
	}
}

func TestVirtualEntryFlags(t *testing.T) {
	image := createTestImage(t)
	exfat, err := libxfat.New(image, false)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	virtuals, err := exfat.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir error: %v", err)
	}

	for _, entry := range virtuals {
		if entry.Name() == "$MBR" || entry.Name() == "$FAT1" || entry.Name() == "$OrphanFiles" {
			if !entry.IsVirtualEntry() {
				t.Errorf("entry %s should be recognized as virtual", entry.Name())
			}
		}
	}
}
