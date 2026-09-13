package test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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
func openCorpusImage(t *testing.T, path string) (*libxfat.ExFAT, int64, bool) {
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
		return nil, 0, false
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
		return fs, size, true
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
			return fs, size, true
		}
	}

	t.Errorf("%s: could not open at byte %d: %v", filepath.Base(path), base, err)
	return nil, 0, false
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

			all, err := fs.AllEntries(root)
			if err != nil {
				t.Fatalf("AllEntries(): %v", err)
			}
			if len(all) > corpusMaxEntries {
				t.Logf("image has %d entries; checking the first %d", len(all), corpusMaxEntries)
				all = all[:corpusMaxEntries]
			}
			t.Logf("volume label %q, cluster size %d, %d entries",
				corpusLabel(t, fs), fs.ClusterSize(), len(all))

			clusterSize := fs.ClusterSize()
			upperBound := time.Now().AddDate(1, 0, 0)
			lowerBound := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

			var withOffset, withoutOffset int

			for _, entry := range all {
				name := entry.Name()

				// Timestamps are either absent or a real, plausible instant.
				ts := entry.Timestamps()
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
				if entry.ValidDataSize() > entry.Size() {
					t.Errorf("%s: valid data length %d exceeds size %d",
						name, entry.ValidDataSize(), entry.Size())
				}

				if entry.IsDir() || entry.IsDeleted() || entry.Size() == 0 {
					continue
				}

				// Region entries are byte ranges outside the cluster heap, so
				// they get checked directly rather than through the cluster map.
				if offset, isRegion := entry.RegionOffset(); isRegion {
					if offset+entry.Size() > uint64(size) {
						t.Errorf("%s: region [%d, %d) falls outside a %d byte image",
							name, offset, offset+entry.Size(), size)
					}
					continue
				}

				// Every fragment must land inside the image.
				clusters, tail, err := fs.ClusterList(entry)
				if err != nil {
					t.Errorf("%s: ClusterList(): %v", name, err)
					continue
				}
				if len(clusters) == 0 {
					continue
				}
				for _, cluster := range clusters {
					offset, offErr := fs.ClusterOffset(cluster)
					if offErr != nil {
						t.Errorf("%q: ClusterOffset(%d): %v", name, cluster, offErr)
						continue
					}
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
				if mapped < entry.Size() {
					t.Errorf("%s: %d clusters cover %d bytes, less than the %d byte size",
						name, len(clusters), mapped, entry.Size())
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
			all, err := fs.AllEntries(root)
			if err != nil {
				t.Fatalf("AllEntries(): %v", err)
			}

			dir := t.TempDir()
			var extracted, fragmented, contiguous int
			var budget int64 = corpusMaxExtractedBytes
			var skippedForBudget int

			for _, entry := range all {
				if extracted >= corpusMaxExtractedFiles {
					break
				}
				if entry.IsDir() || entry.IsDeleted() || entry.Size() == 0 {
					continue
				}
				if int64(entry.Size()) > budget {
					skippedForBudget++
					continue
				}

				dst := filepath.Join(dir, "extract.bin")
				if err := fs.ExtractEntryContent(entry, dst); err != nil {
					t.Errorf("%s: ExtractEntryContent(): %v", entry.Name(), err)
					continue
				}
				info, err := os.Stat(dst)
				if err != nil {
					t.Errorf("%s: Stat(): %v", entry.Name(), err)
					continue
				}
				if uint64(info.Size()) != entry.Size() {
					t.Errorf("%s: extracted %d bytes, want %d",
						entry.Name(), info.Size(), entry.Size())
				}

				budget -= info.Size()
				extracted++
				if entry.IsContiguous() {
					contiguous++
				} else {
					fragmented++
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
					t.Errorf("%s: recovered entry does not report IsDeleted", entry.Name())
				}
				// Recovered timestamps go through the same decoder, so they are
				// subject to the same plausibility requirement.
				if modified := entry.ModifiedTime(); !modified.IsZero() {
					if modified.Year() < 1980 || modified.After(time.Now().AddDate(1, 0, 0)) {
						t.Errorf("%s: implausible recovered modified time %v", entry.Name(), modified)
					}
				}
			}

			t.Logf("recovered %d deleted entries", len(deleted))
		})
	}
}

// TestCorpusUpcaseTable reads each volume's own up-case table and checks the
// folding it produces. Real formatters write the table compressed, collapsing
// the identity runs that cover most of Unicode, so this is the only place the
// decompression meets input it did not write itself.
//
// The expectations are deliberately narrow: ASCII case folding, which every
// exFAT up-case table performs, and stability of characters that have no upper
// case. Anything broader would be asserting a particular Unicode version rather
// than the volume's own table.
func TestCorpusUpcaseTable(t *testing.T) {
	for _, path := range corpusImages(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			fs, _, ok := openCorpusImage(t, path)
			if !ok {
				return
			}
			if _, err := fs.ReadRootDir(); err != nil {
				t.Fatalf("ReadRootDir(): %v", err)
			}

			for _, tc := range []struct{ in, want string }{
				{"readme.txt", "README.TXT"},
				{"MiXeD", "MIXED"},
				{"ALREADY", "ALREADY"},
				{"1234-_.", "1234-_."},
			} {
				got, err := fs.UpcaseString(tc.in)
				if err != nil {
					t.Fatalf("UpcaseString(%q): %v", tc.in, err)
				}
				if got != tc.want {
					t.Errorf("UpcaseString(%q) = %q, want %q", tc.in, got, tc.want)
				}
			}

			// Folding must be idempotent and case-insensitive, whatever the
			// table happens to contain.
			for _, name := range []string{"readme.txt", "Ünïcödé.bin", "emoji-\U0001F600"} {
				once, err := fs.UpcaseString(name)
				if err != nil {
					t.Fatalf("UpcaseString(%q): %v", name, err)
				}
				twice, err := fs.UpcaseString(once)
				if err != nil {
					t.Fatalf("UpcaseString(%q): %v", once, err)
				}
				if once != twice {
					t.Errorf("folding %q is not idempotent: %q then %q", name, once, twice)
				}

				lower, err := fs.NameHash(name)
				if err != nil {
					t.Fatalf("NameHash(%q): %v", name, err)
				}
				upper, err := fs.NameHash(once)
				if err != nil {
					t.Fatalf("NameHash(%q): %v", once, err)
				}
				if lower != upper {
					t.Errorf("%q and its folded form %q hash differently: 0x%04x vs 0x%04x",
						name, once, lower, upper)
				}
			}
		})
	}
}

// TestCorpusNameHashes verifies every entry's recorded name hash against the
// volume's own table. A formatter has no reason to write one that disagrees, so
// any mismatch here is either damage in the image or a defect in this library.
func TestCorpusNameHashes(t *testing.T) {
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
			all, err := fs.AllEntries(root)
			if err != nil {
				t.Fatalf("AllEntries(): %v", err)
			}

			var checked, mismatched, skipped int
			for _, entry := range all {
				err := fs.VerifyNameHash(entry)
				switch {
				case errors.Is(err, libxfat.ErrNoNameHash):
					skipped++
				case errors.Is(err, libxfat.ErrNameHashMismatch):
					mismatched++
					if mismatched <= 5 {
						t.Errorf("%v", err)
					}
				case err != nil:
					t.Fatalf("%s: VerifyNameHash(): %v", entry.Name(), err)
				default:
					checked++
				}
			}

			t.Logf("%d name hashes verified, %d mismatched, %d entries carry none",
				checked, mismatched, skipped)
		})
	}
}

// openCorpusImageOptimistic opens the same volume with verification off, so a
// test can compare the two readings of one image.
func openCorpusImageOptimistic(t *testing.T, path string) (*libxfat.ExFAT, bool) {
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
		return nil, false
	}

	fs, err := libxfat.Open(libxfat.Source{Reader: file, Size: info.Size(), Base: base})
	if err != nil {
		t.Errorf("%s: optimistic open at byte %d: %v", filepath.Base(path), base, err)
		return nil, false
	}
	return fs, true
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
				all, err := fs.AllEntries(root)
				if err != nil {
					t.Fatalf("AllEntries(): %v", err)
				}
				if len(all) > corpusMaxEntries {
					all = all[:corpusMaxEntries]
				}
				out := make([]string, 0, len(all))
				for _, entry := range all {
					out = append(out, entry.Name())
				}
				return out
			}

			strict := names(strictFS)
			optimistic := names(optimisticFS)

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
			all, err := strictFS.AllEntries(root)
			if err != nil {
				t.Fatalf("AllEntries(): %v", err)
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

// TestCorpusIdentity checks the entry addressing against real volumes, where
// directories span several clusters and may be fragmented - the conditions the
// hand-built fixture cannot reproduce.
//
// Each entry's reported offset is read back off the image and compared against
// the entry it claims to describe, using the entry-set checksum, which is derived
// from the whole set's bytes and so cannot match a neighbouring set. That makes
// this a check of the addressing itself rather than of the parser agreeing with
// itself.
func TestCorpusIdentity(t *testing.T) {
	for _, path := range corpusImages(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			fs, _, ok := openCorpusImage(t, path)
			if !ok {
				return
			}

			file, err := os.Open(path)
			if err != nil {
				t.Fatalf("reopen %s: %v", path, err)
			}
			t.Cleanup(func() { _ = file.Close() })

			root, err := fs.ReadRootDir()
			if err != nil {
				t.Fatalf("ReadRootDir(): %v", err)
			}
			all, err := fs.AllEntries(root)
			if err != nil {
				t.Fatalf("AllEntries(): %v", err)
			}
			if len(all) > corpusMaxEntries {
				all = all[:corpusMaxEntries]
			}

			record := make([]byte, 32)
			byID := make(map[libxfat.FileID]string, len(all))
			var addressed, identified, unaddressed int

			for _, entry := range all {
				name := entry.Name()

				offset, hasOffset := entry.EntrySetOffset()
				if !hasOffset {
					// Only entries the library synthesises may lack a record: the
					// region entries, and the $OrphanFiles placeholder. Anything
					// read off the volume has a record and must be able to say
					// where it is.
					if !entry.IsVirtualEntry() {
						t.Errorf("%q: no entry-set offset, but it was read from a directory", name)
					}
					unaddressed++
					continue
				}
				addressed++

				if _, err := file.ReadAt(record, offset); err != nil {
					t.Errorf("%q: reading the record at offset %d: %v", name, offset, err)
					continue
				}
				if record[0] != entry.EntryType() {
					t.Errorf("%q: record at %d has type 0x%02x, entry says 0x%02x",
						name, offset, record[0], entry.EntryType())
					continue
				}
				if record[0]&0x7f == 0x05 {
					expected, _, _ := entry.EntrySetChecksums()
					if got := binary.LittleEndian.Uint16(record[2:4]); got != expected {
						t.Errorf("%q: record at %d records checksum 0x%04x, entry expects 0x%04x",
							name, offset, got, expected)
					}
				}

				id, ok := fs.FileID(entry)
				if !ok {
					t.Errorf("%q: addressed at %d but not identifiable", name, offset)
					continue
				}
				if previous, clash := byID[id]; clash {
					t.Errorf("FileID %s is shared by %q and %q", id, previous, name)
					continue
				}
				byID[id] = name
				identified++
			}

			if addressed == 0 {
				t.Fatal("no entry reported an entry-set offset")
			}
			t.Logf("%d entries addressed and verified against their own records, %d identified, %d without a record",
				addressed, identified, unaddressed)
		})
	}
}

// TestCorpusWalkMatchesGetAllEntries cross-checks the new walk against the
// traversal that predates it, on real volumes. The two are independent
// implementations - one recursive with a cycle guard and a depth cap, the other a
// breadth-first loop - so agreeing on the entry set is evidence about both.
//
// Deleted entries are included because AllEntries reports them unconditionally.
// Neither descends into a deleted directory, so the comparison is like for like.
func TestCorpusWalkMatchesGetAllEntries(t *testing.T) {
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
			flat, err := fs.AllEntries(root)
			if err != nil {
				t.Fatalf("AllEntries(): %v", err)
			}

			paths := make(map[string]int)
			walked := 0
			err = fs.WalkWithOptions(context.Background(),
				libxfat.WalkOptions{IncludeDeleted: true},
				func(p string, _ uint32, entry libxfat.Entry) error {
					walked++
					paths[p]++
					if !strings.HasPrefix(p, "/") {
						t.Errorf("path %q is not absolute", p)
					}
					if name := entry.Name(); !strings.HasSuffix(p, name) {
						t.Errorf("path %q does not end in the entry name %q", p, name)
					}
					return nil
				})
			if err != nil {
				t.Fatalf("Walk: %v", err)
			}

			if walked != len(flat) {
				t.Errorf("Walk reported %d entries, AllEntries %d", walked, len(flat))
			}
			for p, n := range paths {
				if n > 1 {
					t.Errorf("path %q reported %d times", p, n)
				}
			}
			t.Logf("%d entries walked, %d distinct paths", walked, len(paths))
		})
	}
}

// corpusLabel is VolumeLabel reduced to a string for logging: a label that cannot
// be read is a note in the log, not a reason to fail a test that is about something
// else.
func corpusLabel(t *testing.T, fs *libxfat.ExFAT) string {
	t.Helper()

	label, err := fs.VolumeLabel()
	if err != nil {
		t.Logf("VolumeLabel(): %v", err)
		return "(unreadable)"
	}
	return label
}
