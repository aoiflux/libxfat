package test

import (
	"bytes"
	"sync"
	"testing"
)

// TestConcurrentVolumeUse is the acceptance gate for moving parse state off the
// volume handle.
//
// Before that change a single *ExFAT could not be shared at all: directory parsing
// kept its cursor, its half-assembled entry and its name buffers on the handle, so
// two goroutines reading two directories corrupted each other's results. The
// library said so, and callers had to open one volume per goroutine.
//
// This exercises all four things that used to be shared: the directory parser, the
// pooled cluster scratch, the lazily published $BitMap and $UpCase entries, and
// the lazily decompressed up-case table. Run under -race, which is where a
// regression here would actually show up.
func TestConcurrentVolumeUse(t *testing.T) {
	fs := openSuperfloppy(t, true)

	root, err := fs.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir: %v", err)
	}

	// Baselines taken serially, to compare the concurrent results against.
	wantAll, err := fs.GetAllEntries(root)
	if err != nil {
		t.Fatalf("GetAllEntries: %v", err)
	}
	wantContent, err := fs.ReadEntry(entryNamed(t, fs, "fragmented.bin"))
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}

	const goroutines = 12
	var wg sync.WaitGroup
	errs := make(chan string, goroutines*4)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()

			// A full tree walk, which is the parser-state case.
			got, err := fs.GetAllEntries(root)
			if err != nil {
				errs <- "GetAllEntries: " + err.Error()
				return
			}
			if len(got) != len(wantAll) {
				errs <- "concurrent walk found a different number of entries"
			}

			// Extent resolution, which is the pooled-scratch case.
			entry := entryNamed(t, fs, "fragmented.bin")
			if _, err := fs.FragmentOffsets(entry); err != nil {
				errs <- "FragmentOffsets: " + err.Error()
			}

			// Content reading, over the same file from every goroutine.
			content, err := fs.ReadEntry(entry)
			if err != nil {
				errs <- "ReadEntry: " + err.Error()
				return
			}
			if !bytes.Equal(content, wantContent) {
				errs <- "concurrent read returned different bytes"
			}

			// Name hashing, which forces the lazy up-case table load. Several
			// goroutines arriving here together is the case that used to be a
			// plain unsynchronised write.
			if err := fs.VerifyNameHash(entry); err != nil {
				errs <- "VerifyNameHash: " + err.Error()
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}

// TestConcurrentUpcaseLoadFromColdVolume forces the lazy load itself to race: a
// freshly opened volume has no up-case table, so every goroutine arrives wanting
// to build one. They must agree on a single answer rather than install different
// tables or latch a failure.
func TestConcurrentUpcaseLoadFromColdVolume(t *testing.T) {
	fs := openSuperfloppy(t, true)

	const goroutines = 16
	var wg sync.WaitGroup
	hashes := make([]uint16, goroutines)
	errs := make(chan string, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			h, err := fs.NameHash("Readme.TXT")
			if err != nil {
				errs <- "NameHash: " + err.Error()
				return
			}
			hashes[n] = h
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Fatal(msg)
	}

	for i := 1; i < goroutines; i++ {
		if hashes[i] != hashes[0] {
			t.Fatalf("goroutine %d computed hash %#x, goroutine 0 got %#x",
				i, hashes[i], hashes[0])
		}
	}
}
