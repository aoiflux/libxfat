package test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aoiflux/libxfat"
)

// TestReadEntryMatchesExtraction is the point of the content API: the bytes read
// in memory must be exactly the bytes extraction writes to disk. Until this
// existed, comparing a file's content meant writing it to a temp directory first.
func TestReadEntryMatchesExtraction(t *testing.T) {
	fs := openSuperfloppy(t, true)

	for _, name := range []string{"fragmented.bin", "readme.txt", "notes.txt"} {
		t.Run(name, func(t *testing.T) {
			entry := entryNamed(t, fs, name)

			inMemory, err := fs.ReadEntry(entry)
			if err != nil {
				t.Fatalf("ReadEntry: %v", err)
			}

			dst := filepath.Join(t.TempDir(), "extracted.bin")
			if err := fs.ExtractEntryContent(entry, dst); err != nil {
				t.Fatalf("ExtractEntryContent: %v", err)
			}
			onDisk, err := os.ReadFile(dst)
			if err != nil {
				t.Fatalf("read extracted file: %v", err)
			}

			if !bytes.Equal(inMemory, onDisk) {
				t.Fatalf("ReadEntry gave %d bytes, extraction gave %d, and they differ",
					len(inMemory), len(onDisk))
			}
			if int64(len(inMemory)) != int64(entry.GetSize()) {
				t.Errorf("read %d bytes, entry records %d", len(inMemory), entry.GetSize())
			}
		})
	}
}

// TestFileReadAtCrossesRuns checks that a read spanning the gap in a fragmented
// file is stitched correctly. A reader that silently stopped at a run boundary
// would still pass a whole-file read, so this reads across the seam deliberately.
func TestFileReadAtCrossesRuns(t *testing.T) {
	fs := openSuperfloppy(t, true)
	entry := entryNamed(t, fs, "fragmented.bin")

	whole, err := fs.ReadEntry(entry)
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}

	file, err := fs.OpenEntry(entry)
	if err != nil {
		t.Fatalf("OpenEntry: %v", err)
	}

	// Straddle the boundary between cluster 12 and cluster 14.
	const span = 200
	at := int64(fgClusterSize - span/2)
	buf := make([]byte, span)
	n, err := file.ReadAt(buf, at)
	if err != nil && err != io.EOF {
		t.Fatalf("ReadAt across the run boundary: %v", err)
	}
	if n != span {
		t.Fatalf("ReadAt returned %d bytes, want %d", n, span)
	}
	if !bytes.Equal(buf, whole[at:at+span]) {
		t.Fatal("bytes read across the run boundary do not match the whole-file read")
	}
}

// TestFileSequentialReadMatchesReadAll exercises the cursor path against the
// random-access path.
func TestFileSequentialReadMatchesReadAll(t *testing.T) {
	fs := openSuperfloppy(t, true)
	entry := entryNamed(t, fs, "fragmented.bin")

	file, err := fs.OpenEntry(entry)
	if err != nil {
		t.Fatalf("OpenEntry: %v", err)
	}

	// A small buffer, so the read crosses runs and refills many times.
	streamed, err := io.ReadAll(io.LimitReader(struct{ io.Reader }{file}, 1<<20))
	if err != nil {
		t.Fatalf("sequential read: %v", err)
	}

	whole, err := fs.ReadEntry(entry)
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}
	if !bytes.Equal(streamed, whole) {
		t.Fatalf("sequential read gave %d bytes, ReadAll gave %d, and they differ",
			len(streamed), len(whole))
	}
}

// TestFileReaderAtIsConcurrencySafe pins the documented property of ReaderAt: no
// cursor, so several goroutines may read one file at once. Run under -race.
func TestFileReaderAtIsConcurrencySafe(t *testing.T) {
	fs := openSuperfloppy(t, true)
	entry := entryNamed(t, fs, "fragmented.bin")

	file, err := fs.OpenEntry(entry)
	if err != nil {
		t.Fatalf("OpenEntry: %v", err)
	}
	ra, err := file.ReaderAt()
	if err != nil {
		t.Fatalf("ReaderAt: %v", err)
	}
	whole, err := fs.ReadEntry(entry)
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(off int64) {
			defer wg.Done()
			buf := make([]byte, 512)
			n, err := ra.ReadAt(buf, off)
			if err != nil && err != io.EOF {
				errs <- err
				return
			}
			if !bytes.Equal(buf[:n], whole[off:off+int64(n)]) {
				errs <- errors.New("concurrent read returned the wrong bytes")
			}
		}(int64(i * 900))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestSectionReaderRefusesFragmented covers the one case where a caller asks for
// something a fragmented file cannot provide. Stitching a section silently would
// hide exactly what the caller asked about.
func TestSectionReaderRefusesFragmented(t *testing.T) {
	fs := openSuperfloppy(t, true)

	fragmented, err := fs.OpenEntry(entryNamed(t, fs, "fragmented.bin"))
	if err != nil {
		t.Fatalf("OpenEntry: %v", err)
	}
	if _, err := fragmented.SectionReader(); !errors.Is(err, libxfat.ErrFragmented) {
		t.Fatalf("SectionReader() error = %v, want ErrFragmented", err)
	}

	// A contiguous file must still get one.
	contiguous, err := fs.OpenEntry(entryNamed(t, fs, "readme.txt"))
	if err != nil {
		t.Fatalf("OpenEntry: %v", err)
	}
	section, err := contiguous.SectionReader()
	if err != nil {
		t.Fatalf("SectionReader() on a contiguous file: %v", err)
	}
	if section.Size() != int64(contiguous.Size()) {
		t.Errorf("section size = %d, want %d", section.Size(), contiguous.Size())
	}
}

// TestOpenEntryRefusesDeleted keeps the boundary explicit: a deleted entry's chain
// is gone, so reading it as though it were intact is not on offer.
func TestOpenEntryRefusesDeleted(t *testing.T) {
	fs := openSuperfloppy(t, true)
	entry := entryNamed(t, fs, "erased.txt (deleted)")

	if !entry.IsDeleted() {
		t.Fatal("fixture entry is not reported as deleted")
	}
	if _, err := fs.OpenEntry(entry); !errors.Is(err, libxfat.ErrDeletedEntry) {
		t.Fatalf("OpenEntry(deleted) error = %v, want ErrDeletedEntry", err)
	}
}

// TestOpenEntryOnZeroLengthEntry checks that an entry with no allocation opens as
// an empty stream rather than an error: nothing to read is not the same as
// unreadable.
func TestOpenEntryOnZeroLengthEntry(t *testing.T) {
	fs := openSuperfloppy(t, true)
	entry := entryNamed(t, fs, "$OrphanFiles")

	file, err := fs.OpenEntry(entry)
	if err != nil {
		t.Fatalf("OpenEntry($OrphanFiles): %v", err)
	}
	data, err := file.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("read %d bytes from a zero-length entry, want 0", len(data))
	}
}

// TestDeletedNoFatChainIsNotAGuess covers the one place libxfat deliberately
// diverges from its FAT sibling.
//
// A deleted entry's chain is freed, so the FAT must not be walked. But when the
// surviving stream extension recorded NoFatChain, the volume itself declared the
// allocation contiguous, and deletion does not erase that record. So the full run
// is located with no opt-in, and Assumed stays false: it is a fact the volume
// wrote down, not a hypothesis the library formed. FAT cannot state this, which is
// why libfat has to offer contiguity as an option there.
func TestDeletedNoFatChainIsNotAGuess(t *testing.T) {
	fs := openSuperfloppy(t, true)
	entry := entryNamed(t, fs, "erased.txt (deleted)")

	if !entry.DoesNotHaveFatChain() {
		t.Skip("fixture deleted entry does not record NoFatChain")
	}

	result, err := fs.FragmentOffsetsWithOptions(entry, libxfat.FragmentOptions{})
	if err != nil {
		t.Fatalf("FragmentOffsetsWithOptions: %v", err)
	}

	if result.Assumed {
		t.Error("Assumed is set for a contiguity the volume itself declared")
	}
	if !result.NoFatChain {
		t.Error("NoFatChain is not set for an entry whose stream extension set it")
	}
	if result.ChainWalked {
		t.Error("ChainWalked is set for a deleted entry; its chain must never be walked")
	}
	if len(result.Ranges) == 0 {
		t.Fatal("no ranges located for a deleted entry that recorded its own layout")
	}
	if result.BytesCovered != int64(entry.GetSize()) {
		t.Errorf("located %d bytes, entry records %d", result.BytesCovered, entry.GetSize())
	}
	if result.Truncated {
		t.Error("Truncated is set despite the whole declared run being located")
	}
}
