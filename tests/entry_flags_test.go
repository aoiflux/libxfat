package test

import (
	"testing"

	"github.com/aoiflux/libxfat"
)

func TestEntryIsVirtualEntry(t *testing.T) {
	image := createTestImage(t)
	exfat, err := libxfat.New(image, false)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	entries, err := exfat.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir error: %v", err)
	}

	virtuals := map[string]bool{}
	regulars := map[string]bool{}
	for _, entry := range entries {
		switch entry.GetName() {
		case "$MBR", "$FAT1", "$OrphanFiles":
			virtuals[entry.GetName()] = entry.IsVirtualEntry()
		case "$BitMap", "$UpCase", "$Volume GUID":
			regulars[entry.GetName()] = entry.IsVirtualEntry()
		}
	}

	for name, isVirtual := range virtuals {
		if !isVirtual {
			t.Errorf("entry %s should be virtual", name)
		}
	}
	for name, isVirtual := range regulars {
		if isVirtual {
			t.Errorf("entry %s should not be virtual", name)
		}
	}
}

func TestEntryIsSpecialFile(t *testing.T) {
	image := createTestImage(t)
	exfat, err := libxfat.New(image, false)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	t.Cleanup(func() {
		_ = image.Close()
	})

	entries, err := exfat.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir error: %v", err)
	}

	found := map[string]bool{}
	for _, entry := range entries {
		if entry.IsSpecialFile() {
			found[entry.GetName()] = true
		}
	}

	expected := []string{"$BitMap", "$UpCase", "$Volume GUID", "$TexFAT", "$ACT", "$MBR", "$FAT1", "$OrphanFiles"}
	for _, name := range expected {
		if !found[name] {
			t.Errorf("entry %s should be special", name)
		}
	}
}
