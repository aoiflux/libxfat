package test

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aoiflux/libxfat"
)

func findEntry(t *testing.T, entries []libxfat.Entry, name string) libxfat.Entry {
	t.Helper()
	for _, e := range entries {
		if e.Name() == name {
			return e
		}
	}
	t.Fatalf("entry %q not found in root directory", name)
	return libxfat.Entry{}
}

func openTestVolume(t *testing.T) (*libxfat.ExFAT, []libxfat.Entry) {
	t.Helper()

	data := testImageBytes()
	fs, err := libxfat.NewFromReaderAt(bytes.NewReader(data), int64(len(data)), false)
	if err != nil {
		t.Fatalf("NewFromReaderAt() error = %v", err)
	}
	entries, err := fs.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir() error = %v", err)
	}
	return fs, entries
}

// TestExtractAllocationBitmap covers the metadata streams that were previously
// listed but refused by ExtractEntryContent.
func TestExtractAllocationBitmap(t *testing.T) {
	fs, entries := openTestVolume(t)

	bitmap := findEntry(t, entries, "$BitMap")
	if !bitmap.IsMetadataStream() {
		t.Fatal("$BitMap should report IsMetadataStream")
	}

	dst := filepath.Join(t.TempDir(), "bitmap.bin")
	if err := fs.ExtractEntryContent(bitmap, dst); err != nil {
		t.Fatalf("ExtractEntryContent($BitMap) error = %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	// The test volume marks a single allocated cluster in the bitmap.
	if want := []byte{0x05}; !bytes.Equal(got, want) {
		t.Fatalf("extracted $BitMap = %v, want %v", got, want)
	}
}

// TestExtractBootRegion covers the synthetic region-backed entries, which
// previously computed a nonsense offset from a zero entry cluster.
func TestExtractBootRegion(t *testing.T) {
	fs, entries := openTestVolume(t)

	mbr := findEntry(t, entries, "$MBR")
	if !mbr.IsRegion() {
		t.Fatal("$MBR should report IsRegion")
	}
	if want := uint64(12 * testSectorSize); mbr.Size() != want {
		t.Fatalf("$MBR size = %d, want %d", mbr.Size(), want)
	}

	dst := filepath.Join(t.TempDir(), "mbr.bin")
	if err := fs.ExtractEntryContent(mbr, dst); err != nil {
		t.Fatalf("ExtractEntryContent($MBR) error = %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if uint64(len(got)) != mbr.Size() {
		t.Fatalf("extracted $MBR length = %d, want %d", len(got), mbr.Size())
	}
	if string(got[3:11]) != "EXFAT   " {
		t.Fatalf("extracted $MBR signature = %q, want %q", got[3:11], "EXFAT   ")
	}
}

func TestExtractFirstFat(t *testing.T) {
	fs, entries := openTestVolume(t)

	fat := findEntry(t, entries, "$FAT1")
	dst := filepath.Join(t.TempDir(), "fat1.bin")
	if err := fs.ExtractEntryContent(fat, dst); err != nil {
		t.Fatalf("ExtractEntryContent($FAT1) error = %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	want := testImageBytes()[testFatOffsetSector*testSectorSize:][:testFatSizeSectors*testSectorSize]
	if !bytes.Equal(got, want) {
		t.Fatal("extracted $FAT1 does not match the on-image FAT")
	}
}

// TestExtractZeroLengthEntry pins the behaviour that used to underflow the
// cluster offset arithmetic: no allocation means an empty file, not garbage.
func TestExtractZeroLengthEntry(t *testing.T) {
	fs, entries := openTestVolume(t)

	orphans := findEntry(t, entries, "$OrphanFiles")
	if orphans.Size() != 0 {
		t.Fatalf("$OrphanFiles size = %d, want 0", orphans.Size())
	}

	dst := filepath.Join(t.TempDir(), "orphans.bin")
	if err := fs.ExtractEntryContent(orphans, dst); err != nil {
		t.Fatalf("ExtractEntryContent($OrphanFiles) error = %v", err)
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("extracted $OrphanFiles size = %d, want 0", info.Size())
	}
}

// TestConcurrentVolumesShareReader checks the concurrency property the
// io.ReaderAt conversion actually buys: independent volumes over one reader,
// with no shared seek cursor. Run under -race.
func TestConcurrentVolumesShareReader(t *testing.T) {
	data := testImageBytes()
	shared := bytes.NewReader(data)

	var wg sync.WaitGroup
	errs := make([]error, 8)

	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fs, err := libxfat.NewFromReaderAt(shared, int64(len(data)), false)
			if err != nil {
				errs[i] = err
				return
			}
			for range 4 {
				if _, err := fs.ReadRootDir(); err != nil {
					errs[i] = err
					return
				}
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
}
