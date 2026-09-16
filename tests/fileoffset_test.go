package test

import (
	"bytes"
	"testing"

	"github.com/aoiflux/libxfat"
)

// openSuperfloppyInMemory opens the fixture over its own bytes, so a test can
// read the image at an absolute offset and check that a Range points where it
// claims. openSuperfloppy hands back only the volume, which is enough to ask what
// the extents are but not to verify them against the medium.
func openSuperfloppyInMemory(t *testing.T) (*libxfat.ExFAT, []byte) {
	t.Helper()

	image := buildSuperfloppyImage()
	fs, err := libxfat.Open(libxfat.Source{
		Reader:                bytes.NewReader(image),
		Size:                  int64(len(image)),
		Strict:                true,
		IgnorePartitionOffset: true,
	})
	if err != nil {
		t.Fatalf("open fixture from memory: %v", err)
	}
	return fs, image
}

// readSpan returns the image bytes a Range describes, failing the test when the
// range falls outside the image rather than panicking on the slice.
func readSpan(t *testing.T, image []byte, r libxfat.Range) []byte {
	t.Helper()

	if r.StartByte < 0 || r.EndByte() > int64(len(image)) {
		t.Fatalf("range %s falls outside the %d-byte image", r, len(image))
	}
	return image[r.StartByte:r.EndByte()]
}

// TestRangeFileOffsetIsCumulative is the X9 acceptance case. exFAT has no holes,
// so the invariant that matters is that FileOffset is exactly the sum of the
// preceding runs' lengths, and that the runs, read from the image in order,
// reproduce the file.
//
// fragmented.bin is 8000 bytes chaining cluster 12 to cluster 14, so the gap at
// 13 means a reader that merged the runs or visited them out of order produces
// different bytes.
func TestRangeFileOffsetIsCumulative(t *testing.T) {
	fs, image := openSuperfloppyInMemory(t)
	entry := entryNamed(t, fs, "fragmented.bin")

	ranges, err := fs.FragmentOffsets(entry)
	if err != nil {
		t.Fatalf("FragmentOffsets: %v", err)
	}
	if len(ranges) < 2 {
		t.Fatalf("the fixture produced %d run(s); this test needs a fragmented file", len(ranges))
	}

	var (
		want    int64
		rebuilt []byte
	)
	for i, r := range ranges {
		if r.FileOffset != want {
			t.Errorf("run %d: FileOffset = %d, want %d (the sum of the preceding lengths)",
				i, r.FileOffset, want)
		}
		if r.Sparse {
			t.Errorf("run %d is marked sparse; exFAT has no sparse allocation", i)
		}
		rebuilt = append(rebuilt, readSpan(t, image, r)...)
		want += r.Length
	}

	if want != int64(entry.Size()) {
		t.Errorf("the runs sum to %d bytes, want %d: the slice is not gap-free",
			want, entry.Size())
	}

	// Against the library's own content read, so the two paths to a file's bytes
	// - walking the extents by hand and asking for the content - have to agree.
	content, err := fs.ReadEntry(entry)
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}
	if !bytes.Equal(rebuilt, content) {
		t.Fatalf("concatenating the runs did not reproduce the file: %d bytes vs %d",
			len(rebuilt), len(content))
	}

	// And against the fill bytes the fixture uses precisely so that reading the
	// first cluster twice is distinguishable from reading both.
	if !bytes.Equal(rebuilt[:fgClusterSize], bytes.Repeat([]byte{'F'}, fgClusterSize)) {
		t.Error("the first run's bytes are not the first cluster's fill")
	}
	if !bytes.Equal(rebuilt[fgClusterSize:], bytes.Repeat([]byte{'f'}, fgSecondLen)) {
		t.Error("the second run's bytes are not the second cluster's fill")
	}
}

// TestCoalesceRenumbersFileOffsets covers the exported helper's documented
// promise. A caller assembling a range list from another source relies on it,
// and the input's own FileOffsets describe a slice that no longer exists once
// runs have been merged.
func TestCoalesceRenumbersFileOffsets(t *testing.T) {
	merged := libxfat.Coalesce([]libxfat.Range{
		{StartByte: 100, Length: 10, FileOffset: 999},
		{StartByte: 110, Length: 10, FileOffset: 999}, // adjacent, so it merges
		{StartByte: 500, Length: 7, FileOffset: 999},
	})

	if len(merged) != 2 {
		t.Fatalf("Coalesce produced %d runs, want 2: %v", len(merged), merged)
	}
	if merged[0].FileOffset != 0 {
		t.Errorf("first run FileOffset = %d, want 0", merged[0].FileOffset)
	}
	if merged[0].Length != 20 {
		t.Errorf("merged run Length = %d, want 20", merged[0].Length)
	}
	if merged[1].FileOffset != 20 {
		t.Errorf("second run FileOffset = %d, want 20", merged[1].FileOffset)
	}

	// The short-circuit path renumbers too, or a one-run slice would keep a
	// stale offset that every other path clears.
	single := libxfat.Coalesce([]libxfat.Range{{StartByte: 8, Length: 4, FileOffset: 77}})
	if single[0].FileOffset != 0 {
		t.Errorf("single-run FileOffset = %d, want 0", single[0].FileOffset)
	}
}

// TestSlackRangeFileOffsetIsTheDataLength pins the one Range this package
// returns whose FileOffset is not a position inside the file: slack begins where
// the content ends.
func TestSlackRangeFileOffsetIsTheDataLength(t *testing.T) {
	fs := openSuperfloppy(t, true)
	entry := entryNamed(t, fs, "fragmented.bin")

	slack, ok, err := fs.SlackRange(entry)
	if err != nil {
		t.Fatalf("SlackRange: %v", err)
	}
	if !ok {
		t.Fatal("expected slack for a file that does not end on a cluster boundary")
	}

	if slack.FileOffset != int64(entry.Size()) {
		t.Errorf("slack FileOffset = %d, want the recorded size %d", slack.FileOffset, entry.Size())
	}
	if slack.Length != fgSlackLen {
		t.Errorf("slack Length = %d, want %d", slack.Length, fgSlackLen)
	}

	ranges, err := fs.FragmentOffsets(entry)
	if err != nil {
		t.Fatalf("FragmentOffsets: %v", err)
	}
	last := ranges[len(ranges)-1]
	if slack.StartByte != last.EndByte() {
		t.Errorf("slack starts at %d, want %d, where the last content run ends",
			slack.StartByte, last.EndByte())
	}
	if last.FileOffset+last.Length != slack.FileOffset {
		t.Errorf("the last run ends at file offset %d but slack begins at %d",
			last.FileOffset+last.Length, slack.FileOffset)
	}
}

// TestUnwrittenRangeFileOffsetsStartAtValidDataLength covers the case the sibling
// FAT library has no equivalent for: FAT records no valid-data boundary, so the
// numbering of an unwritten tail is an exFAT question only.
//
// unwritten.bin is 3000 bytes with only the first 100 ever written.
func TestUnwrittenRangeFileOffsetsStartAtValidDataLength(t *testing.T) {
	fs, image := openSuperfloppyInMemory(t)
	entry := entryNamed(t, fs, "unwritten.bin")

	unwritten, err := fs.UnwrittenRanges(entry)
	if err != nil {
		t.Fatalf("UnwrittenRanges: %v", err)
	}
	if len(unwritten) == 0 {
		t.Fatal("no unwritten ranges for a file whose ValidDataLength is short of its size")
	}

	want := int64(entry.ValidDataSize())
	for i, r := range unwritten {
		if r.FileOffset != want {
			t.Errorf("unwritten run %d: FileOffset = %d, want %d", i, r.FileOffset, want)
		}
		readSpan(t, image, r) // the run has to be inside the image to be readable at all
		want += r.Length
	}
	if want != int64(entry.Size()) {
		t.Errorf("the unwritten runs end at file offset %d, want the recorded size %d",
			want, entry.Size())
	}

	// The first unwritten byte is one past the last valid one, in both coordinate
	// systems, which is what makes the file offset and the image offset agree.
	content, err := fs.FragmentOffsets(entry)
	if err != nil {
		t.Fatalf("FragmentOffsets: %v", err)
	}
	if got, want := unwritten[0].StartByte, content[0].StartByte+int64(entry.ValidDataSize()); got != want {
		t.Errorf("the unwritten region starts at image byte %d, want %d", got, want)
	}
}

// TestReportFragmentsCarryTheRangeFileOffsets pins the document against the API.
// The report used to derive the file-relative offset itself; it now reads the one
// the Range carries, and the two representations must not be able to disagree.
func TestReportFragmentsCarryTheRangeFileOffsets(t *testing.T) {
	fs := openSuperfloppy(t, true)

	report, err := fs.ReportWithOptions("superfloppy.exfat", libxfat.ReportOptions{
		IncludeSlack:     true,
		IncludeUnwritten: true,
	})
	if err != nil {
		t.Fatalf("ReportWithOptions: %v", err)
	}

	var checkedSlack, checkedUnwritten int
	for _, row := range report.Files {
		var want int64
		for i, frag := range row.Fragments {
			if frag.FileOffset != want {
				t.Errorf("%s fragment %d: file_offset = %d, want %d",
					row.Path, i, frag.FileOffset, want)
			}
			want += frag.Length
		}

		// Slack follows the file's last byte, so it is numbered from the
		// recorded size rather than from the end of the fragments, which stop
		// at the same place only when nothing was truncated.
		if row.Slack != nil {
			checkedSlack++
			if row.Slack.FileOffset != row.Size {
				t.Errorf("%s slack: file_offset = %d, want the recorded size %d",
					row.Path, row.Slack.FileOffset, row.Size)
			}
		}

		if len(row.Unwritten) > 0 {
			checkedUnwritten++
			if row.Unwritten[0].FileOffset != row.Layout.ValidBytes {
				t.Errorf("%s unwritten: first file_offset = %d, want valid_bytes %d",
					row.Path, row.Unwritten[0].FileOffset, row.Layout.ValidBytes)
			}
		}
	}

	// The fixture has both, so a change that stopped emitting either would
	// otherwise leave this test passing vacuously.
	if checkedSlack == 0 {
		t.Error("no row carried slack; the assertions above checked nothing")
	}
	if checkedUnwritten == 0 {
		t.Error("no row carried an unwritten tail; the assertions above checked nothing")
	}
}
