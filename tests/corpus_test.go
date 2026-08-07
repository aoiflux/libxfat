package test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoiflux/libxfat"
)

// The synthetic image in this package is deliberately tiny: a single-cluster
// root directory, no subdirectories, no fragmented files. That leaves the parts
// of the library a real investigation leans on hardest - FAT chain walking,
// multi-cluster directories, fragmented extraction - untested against anything
// a real formatter produced.
//
// This harness closes that gap without committing binary fixtures to the
// repository. Point LIBXFAT_CORPUS at a directory of exFAT images and the tests
// below will exercise each one:
//
//	LIBXFAT_CORPUS=/path/to/images go test ./tests/ -run Corpus -v
//
// Images may be raw volume dumps or whole-disk images with a partition table;
// the harness locates the volume boot record either way. Good corpus material
// includes volumes formatted by Windows, by macOS, and by mkfs.exfat, since the
// three disagree about defaults such as the number of FATs, the sector size and
// whether a UTC offset is recorded at all.
const corpusEnvVar = "LIBXFAT_CORPUS"

// Caps keep a large image from turning into a multi-minute test. Whatever they
// drop is logged rather than silently skipped.
const (
	corpusMaxEntries        = 20000
	corpusMaxExtractedFiles = 25
	corpusMaxExtractedBytes = 64 << 20
	corpusSignatureScanSpan = 64 << 20
)

func corpusImages(t *testing.T) []string {
	t.Helper()

	dir := os.Getenv(corpusEnvVar)
	if dir == "" {
		t.Skipf("set %s to a directory of exFAT images to run the corpus tests", corpusEnvVar)
	}

	var images []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() < int64(12*512) {
			return nil
		}
		images = append(images, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	if len(images) == 0 {
		t.Fatalf("%s=%s contains no candidate images", corpusEnvVar, dir)
	}
	return images
}

// findVolumeBase locates the exFAT volume boot record, so the harness accepts
// both raw volume dumps and whole-disk images with a partition table.
func findVolumeBase(r io.ReaderAt, size int64) (int64, bool) {
	span := size
	if span > corpusSignatureScanSpan {
		span = corpusSignatureScanSpan
	}

	buf := make([]byte, 512)
	for offset := int64(0); offset+512 <= span; offset += 512 {
		n, err := r.ReadAt(buf, offset)
		if n < 512 {
			if err != nil {
				break
			}
			continue
		}
		if bytes.Equal(buf[3:11], []byte("EXFAT   ")) &&
			buf[510] == 0x55 && buf[511] == 0xAA {
			return offset, true
		}
	}
	return 0, false
}

// openCorpusImage opens an image the way a consumer would, preferring strict
// mode and recording which settings the volume actually needed.
func openCorpusImage(t *testing.T, path string) (libxfat.ExFAT, int64, bool) {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = file.Close() })

	info, err := file.Stat()
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	size := info.Size()

	base, found := findVolumeBase(file, size)
	if !found {
		t.Logf("%s: no exFAT volume boot record found in the first %d bytes; skipping",
			filepath.Base(path), corpusSignatureScanSpan)
		return libxfat.ExFAT{}, 0, false
	}

	// Strict, with the offset derived from where the signature actually is.
	fs, err := libxfat.Open(libxfat.Source{
		Reader: file,
		Size:   size,
		Base:   base,
		Strict: true,
	})
	if err == nil {
		t.Logf("%s: opened strict at byte %d", filepath.Base(path), base)
		return *fs, size, true
	}

	// A PartitionOffset disagreement is common and benign: plenty of tools write
	// zero there regardless of where the volume sits.
	if errors.Is(err, libxfat.ErrPartitionOffsetMismatch) {
		fs, err = libxfat.Open(libxfat.Source{
			Reader:                file,
			Size:                  size,
			Base:                  base,
			Strict:                true,
			IgnorePartitionOffset: true,
		})
		if err == nil {
			t.Logf("%s: opened strict at byte %d (PartitionOffset ignored)", filepath.Base(path), base)
			return *fs, size, true
		}
	}

	t.Errorf("%s: could not open at byte %d: %v", filepath.Base(path), base, err)
	return libxfat.ExFAT{}, 0, false
}

// TestCorpusWalk parses every image in the corpus and checks the invariants a
// consumer relies on: entries resolve, fragments land inside the image, and
// timestamps are either absent or plausible.
func TestCorpusWalk(t *testing.T) {
	for _, path := range corpusImages(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			fs, size, ok := openCorpusImage(t, path)
			if !ok {
				return
			}

			root, err := fs.ReadRootDir()
			if err != nil {
				t.Fatalf("ReadRootDir(): %v", err)
			}
			if len(root) == 0 {
				t.Fatal("ReadRootDir() returned no entries")
			}

			all, err := fs.GetAllEntries(root)
			if err != nil {
				t.Fatalf("GetAllEntries(): %v", err)
			}
			if len(all) > corpusMaxEntries {
				t.Logf("image has %d entries; checking the first %d", len(all), corpusMaxEntries)
				all = all[:corpusMaxEntries]
			}
			t.Logf("volume label %q, cluster size %d, %d entries",
				fs.GetVolumeLabel(), fs.GetClusterSize(), len(all))

			clusterSize := fs.GetClusterSize()
			upperBound := time.Now().AddDate(1, 0, 0)
			lowerBound := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

			var withOffset, withoutOffset int

			for _, entry := range all {
				name := entry.GetName()

				// Timestamps are either absent or a real, plausible instant.
				ts := entry.GetTimestamps()
				for label, value := range map[string]time.Time{
					"modified": ts.Modified,
					"created":  ts.Created,
					"accessed": ts.Accessed,
				} {
					if value.IsZero() {
						continue
					}
					if value.Before(lowerBound) || value.After(upperBound) {
						t.Errorf("%s: %s time %v outside [%v, %v]",
							name, label, value, lowerBound, upperBound)
					}
				}
				if ts.ModifiedOffsetValid {
					withOffset++
				} else if !ts.Modified.IsZero() {
					withoutOffset++
				}

				// Valid data length never exceeds the allocated length.
				if entry.GetValidDataSize() > entry.GetSize() {
					t.Errorf("%s: valid data length %d exceeds size %d",
						name, entry.GetValidDataSize(), entry.GetSize())
				}

				if entry.IsDir() || entry.IsDeleted() || entry.GetSize() == 0 {
					continue
				}

				// Region entries are byte ranges outside the cluster heap, so
				// they get checked directly rather than through the cluster map.
				if offset, isRegion := entry.GetRegionOffset(); isRegion {
					if offset+entry.GetSize() > uint64(size) {
						t.Errorf("%s: region [%d, %d) falls outside a %d byte image",
							name, offset, offset+entry.GetSize(), size)
					}
					continue
				}

				// Every fragment must land inside the image.
				clusters, tail, err := fs.GetClusterList(entry)
				if err != nil {
					t.Errorf("%s: GetClusterList(): %v", name, err)
					continue
				}
				if len(clusters) == 0 {
					continue
				}
				for _, cluster := range clusters {
					offset := fs.GetClusterOffset(cluster)
					if offset > uint64(size) || offset+clusterSize > uint64(size) {
						t.Errorf("%s: cluster %d maps to offset %d, outside a %d byte image",
							name, cluster, offset, size)
						break
					}
				}
				if tail == 0 || tail > clusterSize {
					t.Errorf("%s: tail length %d not in (0, %d]", name, tail, clusterSize)
				}

				// The mapped clusters must account for the file's size.
				mapped := uint64(len(clusters)-1)*clusterSize + tail
				if mapped < entry.GetSize() {
					t.Errorf("%s: %d clusters cover %d bytes, less than the %d byte size",
						name, len(clusters), mapped, entry.GetSize())
				}
			}

			t.Logf("timestamps: %d anchored by a UTC offset, %d unanchored", withOffset, withoutOffset)
		})
	}
}

// TestCorpusExtract checks that extracted content is exactly the length the
// entry advertises, which is where fragmented files and the contiguous fast
// path most easily disagree.
func TestCorpusExtract(t *testing.T) {
	for _, path := range corpusImages(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			fs, _, ok := openCorpusImage(t, path)
			if !ok {
				return
			}

			root, err := fs.ReadRootDir()
			if err != nil {
				t.Fatalf("ReadRootDir(): %v", err)
			}
			all, err := fs.GetAllEntries(root)
			if err != nil {
				t.Fatalf("GetAllEntries(): %v", err)
			}

			dir := t.TempDir()
			var extracted, fragmented, contiguous int
			var budget int64 = corpusMaxExtractedBytes
			var skippedForBudget int

			for _, entry := range all {
				if extracted >= corpusMaxExtractedFiles {
					break
				}
				if entry.IsDir() || entry.IsDeleted() || entry.GetSize() == 0 {
					continue
				}
				if int64(entry.GetSize()) > budget {
					skippedForBudget++
					continue
				}

				dst := filepath.Join(dir, "extract.bin")
				if err := fs.ExtractEntryContent(entry, dst); err != nil {
					t.Errorf("%s: ExtractEntryContent(): %v", entry.GetName(), err)
					continue
				}
				info, err := os.Stat(dst)
				if err != nil {
					t.Errorf("%s: Stat(): %v", entry.GetName(), err)
					continue
				}
				if uint64(info.Size()) != entry.GetSize() {
					t.Errorf("%s: extracted %d bytes, want %d",
						entry.GetName(), info.Size(), entry.GetSize())
				}

				budget -= info.Size()
				extracted++
				if entry.HasFatChain() {
					fragmented++
				} else {
					contiguous++
				}
			}

			t.Logf("extracted %d files (%d FAT-chained, %d contiguous)", extracted, fragmented, contiguous)
			if skippedForBudget > 0 {
				t.Logf("skipped %d files larger than the remaining %d byte budget",
					skippedForBudget, corpusMaxExtractedBytes)
			}
			if extracted == 0 {
				t.Log("no extractable regular files found in this image")
			}
			if fragmented == 0 {
				t.Log("no FAT-chained files in this image; chain walking was not exercised")
			}
		})
	}
}

// TestCorpusRecoverDeleted exercises orphan discovery, which scans every
// unallocated cluster and is the most expensive path in the library.
func TestCorpusRecoverDeleted(t *testing.T) {
	if testing.Short() {
		t.Skip("deleted entry recovery scans the whole unallocated area")
	}

	for _, path := range corpusImages(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			fs, _, ok := openCorpusImage(t, path)
			if !ok {
				return
			}

			if _, err := fs.ReadRootDir(); err != nil {
				t.Fatalf("ReadRootDir(): %v", err)
			}

			deleted, err := fs.RecoverDeletedEntries()
			if err != nil {
				t.Fatalf("RecoverDeletedEntries(): %v", err)
			}

			for _, entry := range deleted {
				if !entry.IsDeleted() {
					t.Errorf("%s: recovered entry does not report IsDeleted", entry.GetName())
				}
				// Recovered timestamps go through the same decoder, so they are
				// subject to the same plausibility requirement.
				if modified := entry.GetModifiedTime(); !modified.IsZero() {
					if modified.Year() < 1980 || modified.After(time.Now().AddDate(1, 0, 0)) {
						t.Errorf("%s: implausible recovered modified time %v", entry.GetName(), modified)
					}
				}
			}

			t.Logf("recovered %d deleted entries", len(deleted))
		})
	}
}

// openCorpusImageOptimistic opens the same volume with verification off, so a
// test can compare the two readings of one image.
func openCorpusImageOptimistic(t *testing.T, path string) (libxfat.ExFAT, bool) {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = file.Close() })

	info, err := file.Stat()
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}

	base, found := findVolumeBase(file, info.Size())
	if !found {
		return libxfat.ExFAT{}, false
	}

	fs, err := libxfat.Open(libxfat.Source{Reader: file, Size: info.Size(), Base: base})
	if err != nil {
		t.Errorf("%s: optimistic open at byte %d: %v", filepath.Base(path), base, err)
		return libxfat.ExFAT{}, false
	}
	return *fs, true
}

// TestCorpusStrictMatchesOptimistic is a standing differential invariant:
// turning verification on must change what the library reports about the
// volume, never what it finds in it.
//
// Strict mode used to fail this on every image, because a checksum computed
// over the wrong byte range made verification fail for every entry set and the
// failure path replaced each name with an empty string.
func TestCorpusStrictMatchesOptimistic(t *testing.T) {
	for _, path := range corpusImages(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			strictFS, _, ok := openCorpusImage(t, path)
			if !ok {
				return
			}
			optimisticFS, ok := openCorpusImageOptimistic(t, path)
			if !ok {
				return
			}

			names := func(fs *libxfat.ExFAT) []string {
				root, err := fs.ReadRootDir()
				if err != nil {
					t.Fatalf("ReadRootDir(): %v", err)
				}
				all, err := fs.GetAllEntries(root)
				if err != nil {
					t.Fatalf("GetAllEntries(): %v", err)
				}
				if len(all) > corpusMaxEntries {
					all = all[:corpusMaxEntries]
				}
				out := make([]string, 0, len(all))
				for _, entry := range all {
					out = append(out, entry.GetName())
				}
				return out
			}

			strict := names(&strictFS)
			optimistic := names(&optimisticFS)

			if len(strict) != len(optimistic) {
				t.Fatalf("strict found %d entries, optimistic %d", len(strict), len(optimistic))
			}

			var blank, mismatched int
			for i := range strict {
				if strict[i] != optimistic[i] {
					t.Errorf("entry %d: strict %q, optimistic %q", i, strict[i], optimistic[i])
					mismatched++
					if mismatched > 10 {
						t.Fatal("too many name differences; stopping")
					}
				}
				if strict[i] == "" {
					blank++
				}
			}
			if blank > 0 {
				t.Errorf("%d of %d entries have an empty name in strict mode", blank, len(strict))
			}

			// Report how much of the volume actually verified, so a corpus run
			// says something about image integrity rather than only about parity.
			root, err := strictFS.ReadRootDir()
			if err != nil {
				t.Fatalf("ReadRootDir(): %v", err)
			}
			all, err := strictFS.GetAllEntries(root)
			if err != nil {
				t.Fatalf("GetAllEntries(): %v", err)
			}
			var verified, failed int
			for _, entry := range all {
				switch {
				case entry.NameChecksumVerified():
					verified++
				case entry.NameChecksumMismatch():
					failed++
					if failed <= 5 {
						t.Logf("checksum mismatch: %v", entry.NameChecksumError())
					}
				}
			}
			t.Logf("%d entry sets verified, %d failed", verified, failed)
		})
	}
}
