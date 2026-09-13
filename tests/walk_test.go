package test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aoiflux/libxfat"
)

// walkRecord is one callback invocation, kept whole so a test can assert on the
// relationship between the arguments rather than only on the paths.
type walkRecord struct {
	path   string
	parent uint32
	entry  libxfat.Entry
}

func walkAll(t *testing.T, fs *libxfat.ExFAT, opts libxfat.WalkOptions) []walkRecord {
	t.Helper()

	var seen []walkRecord
	err := fs.WalkWithOptions(context.Background(), opts,
		func(path string, parent uint32, entry libxfat.Entry) error {
			seen = append(seen, walkRecord{path, parent, entry})
			return nil
		})
	if err != nil {
		t.Fatalf("WalkWithOptions(%+v): %v", opts, err)
	}
	return seen
}

func walkPaths(records []walkRecord) []string {
	paths := make([]string, 0, len(records))
	for _, record := range records {
		paths = append(paths, record.path)
	}
	return paths
}

// TestWalkReportsTheTreeInDiskOrder pins the whole default result, in order. The
// order is part of the contract - pre-order, and within a directory the order the
// records physically appear in - so asserting a sorted set would not test it.
//
// The list includes /fragmented.bin deliberately. ContiguousFiles and
// ContiguousFilePaths both exclude every file with a FAT chain, so that
// entry is missing from their results; a walk that inherited the filter would be
// unusable for change detection, which is the purpose this one exists for.
func TestWalkReportsTheTreeInDiskOrder(t *testing.T) {
	fs := openSuperfloppy(t, true)

	want := []string{
		"/$BitMap",
		"/$UpCase",
		"/readme.txt",
		"/" + sfLongName,
		"/" + sfUnicodeName,
		"/docs",
		"/docs/nested",
		"/docs/nested/deep.bin",
		"/docs/notes.txt",
		"/docs/" + sfUnnamedDirName,
		"/docs/" + sfUnnamedDirName + "/buried.txt",
		"/unwritten.bin",
		"/threerun.bin",
		"/descending.bin",
		"/fragmented.bin",
		"/$MBR",
		"/$FAT1",
		"/$OrphanFiles",
	}

	got := walkPaths(walkAll(t, fs, libxfat.WalkOptions{}))
	if len(got) != len(want) {
		t.Fatalf("walk reported %d entries:\n got %q\nwant %q", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestWalkLeavesTheEntryAlone is the X5 regression. ContiguousFilePaths
// composes the path by overwriting the entry's name, so after it runs the bare
// name is gone and the entry no longer answers to anything that reads the name.
// The walk hands the path over as an argument and leaves the entry untouched.
func TestWalkLeavesTheEntryAlone(t *testing.T) {
	fs := openSuperfloppy(t, true)

	for _, record := range walkAll(t, fs, libxfat.WalkOptions{}) {
		name := record.entry.Name()
		if strings.Contains(name, "/") {
			t.Errorf("%q: entry name was rewritten into a path", name)
		}
		if !strings.HasSuffix(record.path, name) {
			t.Errorf("path %q does not end in the entry's name %q", record.path, name)
		}
	}
}

// TestWalkDoesNotReportTheRoot pins the refusal. The root has no directory record
// on the volume - no name, no timestamps, no parent - so an Entry for it would be
// invented.
func TestWalkDoesNotReportTheRoot(t *testing.T) {
	fs := openSuperfloppy(t, true)

	for _, record := range walkAll(t, fs, libxfat.WalkOptions{}) {
		if record.path == "/" || record.path == "" {
			t.Errorf("walk reported the root itself as %q", record.path)
		}
	}
}

// TestWalkParentAgreesWithTheEntry checks the redundancy in the callback's
// arguments is actually redundant. The parent cluster is passed separately so a
// caller need not reach into the entry, which is only safe if the two cannot
// disagree.
//
// The entries the library synthesises are the documented exception: they are
// reported as part of the root listing, so they arrive with the root's cluster
// while the entry itself has no parent, having no record.
func TestWalkParentAgreesWithTheEntry(t *testing.T) {
	fs := openSuperfloppy(t, true)

	checked := 0
	for _, record := range walkAll(t, fs, libxfat.WalkOptions{}) {
		if _, hasRecord := record.entry.EntrySetOffset(); !hasRecord {
			if record.entry.ParentFirstCluster() != 0 {
				t.Errorf("%q has no record but claims parent cluster %d",
					record.path, record.entry.ParentFirstCluster())
			}
			continue
		}
		if got := record.entry.ParentFirstCluster(); got != record.parent {
			t.Errorf("%q: callback said parent %d, entry says %d",
				record.path, record.parent, got)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no entries with records were checked")
	}
}

// TestWalkDeletedEntriesAreOptIn checks both halves of the default: a deleted
// record is not reported unless asked for, and is reported in its physical
// position among its live siblings when it is.
func TestWalkDeletedEntriesAreOptIn(t *testing.T) {
	fs := openSuperfloppy(t, true)

	const deletedPath = "/docs/erased.txt"

	for _, path := range walkPaths(walkAll(t, fs, libxfat.WalkOptions{})) {
		if strings.HasPrefix(path, deletedPath) {
			t.Fatalf("default walk reported the deleted entry %q", path)
		}
	}

	withDeleted := walkPaths(walkAll(t, fs, libxfat.WalkOptions{IncludeDeleted: true}))

	found := -1
	for i, path := range withDeleted {
		// The path is the recorded name with nothing appended, deletion included,
		// which is what makes it comparable against the same file seen live.
		if path == deletedPath {
			found = i
			break
		}
	}
	if found < 0 {
		t.Fatalf("IncludeDeleted did not report the deleted entry: %q", withDeleted)
	}

	// Disk order: erased.txt sits between notes.txt and the nameless directory.
	if got := withDeleted[found-1]; got != "/docs/notes.txt" {
		t.Errorf("entry before the deleted one = %q, want /docs/notes.txt", got)
	}
	if got := withDeleted[found+1]; got != "/docs/"+sfUnnamedDirName {
		t.Errorf("entry after the deleted one = %q, want /docs/%s", got, sfUnnamedDirName)
	}
}

// TestWalkCancellationIsPaced pins what cancellation actually promises, which is
// weaker than "stops immediately" and deliberately so.
//
// The context is consulted once per check interval rather than once per entry,
// because a check costs more than the work of reporting one entry. So a tree
// smaller than that interval - this fixture is fifteen entries - finishes before
// the second check ever happens, and cancelling mid-walk does not abort it. That
// is not a failure to cancel: the walk was already over.
//
// The two halves of the guarantee are covered elsewhere. That the interval fires
// when it is reached is tested directly on the counter, in the internal
// scanProgress test; that the context is wired through and the first unit is
// always checked is TestWalkAlreadyCancelledReportsNothing below.
func TestWalkCancellationIsPaced(t *testing.T) {
	fs := openSuperfloppy(t, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var seen []string
	err := fs.Walk(ctx, func(path string, _ uint32, _ libxfat.Entry) error {
		seen = append(seen, path)
		cancel()
		return nil
	})
	if err != nil {
		t.Fatalf("Walk over a tree shorter than the check interval = %v, want nil", err)
	}
	if len(seen) == 0 {
		t.Fatal("nothing was reported")
	}
}

// TestWalkCallbackErrorStopsTheWalk checks the immediate stop that is available:
// an error from the callback. It is returned unchanged, with no sentinel to
// unwrap, and nothing is reported after it.
func TestWalkCallbackErrorStopsTheWalk(t *testing.T) {
	fs := openSuperfloppy(t, true)

	sentinel := errors.New("enough")
	count := 0
	err := fs.Walk(context.Background(), func(_ string, _ uint32, _ libxfat.Entry) error {
		count++
		if count == 3 {
			return sentinel
		}
		return nil
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("Walk = %v, want the callback's own error", err)
	}
	if count != 3 {
		t.Errorf("callback ran %d times after returning an error, want 3", count)
	}
}

// TestWalkAlreadyCancelledReportsNothing pins the counter being tested before it
// advances. A context that is already cancelled must be caught on the first entry,
// not on the thousandth.
func TestWalkAlreadyCancelledReportsNothing(t *testing.T) {
	fs := openSuperfloppy(t, true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := fs.Walk(ctx, func(path string, _ uint32, _ libxfat.Entry) error {
		t.Errorf("reported %q despite an already-cancelled context", path)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Walk = %v, want context.Canceled", err)
	}
}

// TestWalkRejectsNilArguments pins the refusals. A nil context becomes a silent
// context.Background() in a great many APIs, which leaves a caller believing an
// operation is interruptible when it is not.
func TestWalkRejectsNilArguments(t *testing.T) {
	fs := openSuperfloppy(t, true)

	//nolint:staticcheck // passing nil is the thing under test
	if err := fs.Walk(nil, func(string, uint32, libxfat.Entry) error { return nil }); !errors.Is(err, libxfat.ErrNilContext) {
		t.Errorf("Walk(nil ctx) = %v, want ErrNilContext", err)
	}
	if err := fs.Walk(context.Background(), nil); !errors.Is(err, libxfat.ErrNilCallback) {
		t.Errorf("Walk(nil fn) = %v, want ErrNilCallback", err)
	}
	//nolint:staticcheck // as above
	if _, err := fs.RecoverDeletedEntriesContext(nil); !errors.Is(err, libxfat.ErrNilContext) {
		t.Errorf("RecoverDeletedEntriesContext(nil) = %v, want ErrNilContext", err)
	}
}

// TestWalkMaxDepthTruncates checks that a depth limit reports the directory
// sitting on the limit and stops reading its contents, rather than failing.
func TestWalkMaxDepthTruncates(t *testing.T) {
	fs := openSuperfloppy(t, true)

	for _, tc := range []struct {
		depth   int
		present string
		absent  string
	}{
		{depth: 1, present: "/docs", absent: "/docs/notes.txt"},
		{depth: 2, present: "/docs/nested", absent: "/docs/nested/deep.bin"},
		{depth: 3, present: "/docs/nested/deep.bin", absent: ""},
	} {
		paths := walkPaths(walkAll(t, fs, libxfat.WalkOptions{MaxDepth: tc.depth}))

		found := make(map[string]bool, len(paths))
		for _, path := range paths {
			found[path] = true
		}
		if !found[tc.present] {
			t.Errorf("MaxDepth %d: %q missing from %q", tc.depth, tc.present, paths)
		}
		if tc.absent != "" && found[tc.absent] {
			t.Errorf("MaxDepth %d: %q should not have been reached", tc.depth, tc.absent)
		}
	}
}

// TestWalkDescendsADeletedDirectory is the acceptance test for the deleted
// descent. /docs/gone is a deleted directory whose first cluster is still free, so
// the records in it survive and the path they had is recoverable.
//
// It also pins the gate: the descent needs both options, because a directory that
// is not itself reported cannot be descended into.
func TestWalkDescendsADeletedDirectory(t *testing.T) {
	fs := openSuperfloppy(t, true)

	const child = "/docs/gone/vanished.txt"

	for _, opts := range []libxfat.WalkOptions{
		{},
		{IncludeDeleted: true},
		{DescendDeletedDirectories: true}, // no effect without IncludeDeleted
	} {
		for _, path := range walkPaths(walkAll(t, fs, opts)) {
			if strings.Contains(path, "vanished") {
				t.Errorf("%+v reported %q without being asked to descend", opts, path)
			}
		}
	}

	paths := walkPaths(walkAll(t, fs, libxfat.WalkOptions{
		IncludeDeleted:            true,
		DescendDeletedDirectories: true,
	}))
	for _, path := range paths {
		if path == child {
			return
		}
	}
	t.Errorf("the deleted directory's child %q was not recovered: %q", child, paths)
}

// TestWalkIncludeRecoveredReportsCarvings checks the free-space sweep folded into
// the walk, and the documented consequence of combining it with the deleted
// descent: one record reported twice, under two different claims.
//
// The pair is not a duplicate to be suppressed. One says "this record still has a
// path"; the other says "nothing on the volume reaches this cluster". A caller that
// wants one row per record collapses them on the entry-set offset, which this test
// checks is in fact the same.
func TestWalkIncludeRecoveredReportsCarvings(t *testing.T) {
	fs := openSuperfloppy(t, true)

	var carved []walkRecord
	for _, record := range walkAll(t, fs, libxfat.WalkOptions{IncludeRecovered: true}) {
		if strings.HasPrefix(record.path, "/$OrphanFiles/") {
			carved = append(carved, record)
		}
	}
	if len(carved) == 0 {
		t.Fatal("IncludeRecovered reported nothing from the unallocated clusters")
	}

	for _, record := range carved {
		if record.parent != 0 {
			t.Errorf("%q: parent cluster %d, want 0 - a carving has no parent directory",
				record.path, record.parent)
		}
		if _, ok := fs.FileID(record.entry); ok {
			t.Errorf("%q: reports a FileID despite having no parent", record.path)
		}
		if _, ok := record.entry.EntrySetOffset(); !ok {
			t.Errorf("%q: no entry-set offset, but the cluster it came from is known",
				record.path)
		}
	}

	// The same record, reached both ways.
	both := walkAll(t, fs, libxfat.WalkOptions{
		IncludeDeleted:            true,
		DescendDeletedDirectories: true,
		IncludeRecovered:          true,
	})

	var offsets []int64
	for _, record := range both {
		if !strings.Contains(record.path, "vanished") {
			continue
		}
		offset, ok := record.entry.EntrySetOffset()
		if !ok {
			t.Fatalf("%q reports no entry-set offset", record.path)
		}
		offsets = append(offsets, offset)
	}
	if len(offsets) != 2 {
		t.Fatalf("vanished.txt was reported %d times, want 2 (once by path, once by sweep)",
			len(offsets))
	}
	if offsets[0] != offsets[1] {
		t.Errorf("the two reports of vanished.txt name different records: %d and %d",
			offsets[0], offsets[1])
	}
}

// TestWalkCycleGuardTerminates checks the guard against a directory reachable from
// inside itself. A valid volume cannot produce one, so the fixture is patched to
// make /docs/nested point back at /docs.
//
// Without the guard this recurses until the depth cap, reporting the same two
// directories four thousand times. With it, each is reported where it is named and
// descended into once.
func TestWalkCycleGuardTerminates(t *testing.T) {
	image := buildSuperfloppyImage()

	// /docs is cluster 7; "nested" is its first entry set, so the stream
	// extension is the second record and its FirstCluster field is at byte 20.
	const streamCluster = 20
	offset := int(sfHeapOffsetSector)*sfSectorSize +
		(sfDocsCluster-2)*sfClusterSize + 32 + streamCluster
	binary.LittleEndian.PutUint32(image[offset:offset+4], sfDocsCluster)

	fs, err := libxfat.Open(libxfat.Source{
		Reader:                bytes.NewReader(image),
		Size:                  int64(len(image)),
		IgnorePartitionOffset: true,
	})
	if err != nil {
		t.Fatalf("open the patched image: %v", err)
	}

	counts := make(map[string]int)
	err = fs.Walk(context.Background(), func(path string, _ uint32, _ libxfat.Entry) error {
		counts[path]++
		if len(counts) > 100 {
			return errors.New("walk did not terminate")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk over a cyclic tree: %v", err)
	}

	// "nested" now names /docs, which the walk already entered on its way in, so
	// it is reported once and not descended into.
	if got := counts["/docs/nested"]; got != 1 {
		t.Errorf("/docs/nested reported %d times, want 1", got)
	}
	if got := counts["/docs/nested/notes.txt"]; got != 0 {
		t.Errorf("the cycle was followed: /docs/nested/notes.txt reported %d times", got)
	}
}

// TestWalkNamesAreUndecorated pins that a reported name is the name the volume
// recorded and nothing else.
//
// Releases before v1.3.0 appended " (deleted)" to a deleted entry's name, which
// put the marker into every path composed from it. That made a deleted record's
// path impossible to compare against the same file seen live, or against another
// tool's output, and the library itself had to strip the suffix back off
// internally to answer HasNoName. IsDeleted is the flag; rendering is the
// caller's business.
func TestWalkNamesAreUndecorated(t *testing.T) {
	fs := openSuperfloppy(t, true)

	records := walkAll(t, fs, libxfat.WalkOptions{
		IncludeDeleted:            true,
		DescendDeletedDirectories: true,
	})

	var sawDeleted bool
	for _, record := range records {
		if strings.Contains(record.path, "(deleted)") {
			t.Errorf("path %q carries a marker the volume never recorded", record.path)
		}
		if !record.entry.IsDeleted() {
			continue
		}
		sawDeleted = true
		// A synthetic placeholder is the one name that is not off the disk, and it
		// is reported through HasSyntheticName rather than in the string.
		if record.entry.HasSyntheticName() {
			continue
		}
		if got, want := record.entry.Name(), record.entry.RawName(); got != want {
			t.Errorf("deleted entry Name() = %q, RawName() = %q; they must agree", got, want)
		}
	}
	if !sawDeleted {
		t.Fatal("walk reported no deleted entries, so nothing was checked")
	}

	// The deleted directory and its child are reachable by their recorded names.
	paths := walkPaths(records)
	for _, want := range []string{"/docs/erased.txt", "/docs/gone", "/docs/gone/vanished.txt"} {
		if !slices.Contains(paths, want) {
			t.Errorf("walk did not report %q; got %q", want, paths)
		}
	}
}
