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

// Clusters used only by the skip image. Every one of them is past the ranges the
// superfloppy and damaged images use, so adding them disturbs nothing those tests
// assert against.
const (
	skEmpty  = 60 // a live directory with nothing in it
	skBad    = 61 // a live directory whose chain runs into a bad cluster
	skOuter  = 62 // holds inner
	skInner  = 63 // holds loop, which names skOuter again
	skBlank  = 64 // a live directory whose name is only spaces
	skReused = 65 // a deleted directory's first cluster, taken again by a live one
	skAfter  = 66
	skGhost  = 67 // a deleted directory whose cluster is still free
	skKept   = 68 // the live directory's child at skReused

	// skFar is far past the end of the cluster heap, so there is nothing to read
	// and no read to fail. The parser refuses a stream extension that names such a
	// cluster while claiming data, so the record that reaches a walk is one whose
	// DataLength is zero - still a directory naming a cluster that is not there.
	skFar = 0x00100000

	// skBadClusterMarker is the FAT value exFAT reserves for a bad cluster.
	skBadClusterMarker = 0xfffffff7

	skBlankName = "   "
)

// buildSkipImage returns the superfloppy volume with one directory for each
// reason a walk declines to descend, an empty directory that must never be
// reported as skipped, and a file after them all so that continuation stays
// observable:
//
//	/empty              cluster 60, no children at all
//	/baddir             cluster 61, two clusters long, but its chain is a bad cluster
//	/fardir             first cluster past the end of the heap, DataLength zero
//	/outer              cluster 62, holds inner
//	/outer/inner        cluster 63, holds loop
//	/outer/inner/loop   names cluster 62 again, its own grandparent
//	/"   "              cluster 64, a name made only of spaces, which ReadDir declines
//	/old                deleted, first cluster 65 - which /new has since taken
//	/new                cluster 65, holds kept.txt
//	/ghost              deleted, cluster 67 still free, holds a deleted "again" naming 67
//	/after.txt
//
// Like buildDamagedImage it is a copy: the bad chain and the cycle forfeit the
// conformance the superfloppy fixture keeps for third-party checkers.
func buildSkipImage(t *testing.T) []byte {
	t.Helper()

	image := buildSuperfloppyImage()

	fat := image[sfFatOffsetSector*sfSectorSize : (sfFatOffsetSector+sfFatSizeSectors)*sfSectorSize]
	setFat := func(cluster, next uint32) {
		binary.LittleEndian.PutUint32(fat[cluster*4:cluster*4+4], next)
	}
	clusterAt := func(cluster uint32) []byte {
		start := sfHeapOffsetSector*sfSectorSize + int(cluster-2)*sfClusterSize
		return image[start : start+sfClusterSize]
	}
	bitmap := clusterAt(sfBitmapCluster)
	markAllocated := func(cluster uint32) {
		index := cluster - 2
		bitmap[index/8] |= 1 << (index % 8)
	}

	for _, cluster := range []uint32{skEmpty, skOuter, skInner, skBlank, skReused, skAfter, skKept} {
		setFat(cluster, 0xffffffff)
		markAllocated(cluster)
	}

	// The directory claims two clusters and the FAT says the first one's successor
	// is bad. Its first cluster holds a real record, so a walk that ignored the
	// failure would have something to show for it.
	setFat(skBad, skBadClusterMarker)
	markAllocated(skBad)
	copy(clusterAt(skBad), sfBuildEntrySet(sfEntrySet{name: "child.txt", attrs: 0x20}))

	copy(clusterAt(skOuter), sfBuildEntrySet(sfEntrySet{
		name: "inner", attrs: 0x10, cluster: skInner, size: sfClusterSize,
	}))
	copy(clusterAt(skInner), sfBuildEntrySet(sfEntrySet{
		name: "loop", attrs: 0x10, cluster: skOuter, size: sfClusterSize,
	}))
	copy(clusterAt(skBlank), sfBuildEntrySet(sfEntrySet{name: "hidden.txt", attrs: 0x20}))
	copy(clusterAt(skReused), sfBuildEntrySet(sfEntrySet{
		name: "kept.txt", attrs: 0x20, cluster: skKept, size: 4, noFatChain: true,
	}))

	// The ghost gets no FAT entry and no bitmap bit: deletion freed it, which is
	// what lets the deleted descent read it. The record inside names the ghost's
	// own cluster, so descending it is a cycle made entirely of deleted records.
	copy(clusterAt(skGhost), sfBuildEntrySet(sfEntrySet{
		name: "again", attrs: 0x10, cluster: skGhost, size: sfClusterSize,
		noFatChain: true, deleted: true,
	}))

	for _, cluster := range []uint32{skAfter, skKept} {
		data := clusterAt(cluster)
		for i := range data {
			data[i] = 'S'
		}
	}

	// Append to the root, after its last record, as buildDamagedImage does.
	root := clusterAt(sfRootCluster)
	offset := 0
	for offset < len(root) && root[offset] != 0x00 {
		offset += 32
	}
	appendSet := func(s sfEntrySet) {
		records := sfBuildEntrySet(s)
		if offset+len(records) > len(root) {
			t.Fatalf("skip root overflows its cluster at %q", s.name)
		}
		copy(root[offset:], records)
		offset += len(records)
	}

	appendSet(sfEntrySet{name: "empty", attrs: 0x10, cluster: skEmpty, size: sfClusterSize})
	appendSet(sfEntrySet{name: "baddir", attrs: 0x10, cluster: skBad, size: 2 * sfClusterSize})
	appendSet(sfEntrySet{name: "fardir", attrs: 0x10, cluster: skFar})
	appendSet(sfEntrySet{name: "outer", attrs: 0x10, cluster: skOuter, size: sfClusterSize})
	appendSet(sfEntrySet{name: skBlankName, attrs: 0x10, cluster: skBlank, size: sfClusterSize})
	appendSet(sfEntrySet{
		name: "old", attrs: 0x10, cluster: skReused, size: sfClusterSize, deleted: true,
	})
	appendSet(sfEntrySet{name: "new", attrs: 0x10, cluster: skReused, size: sfClusterSize})
	appendSet(sfEntrySet{
		name: "ghost", attrs: 0x10, cluster: skGhost, size: sfClusterSize,
		noFatChain: true, deleted: true,
	})
	appendSet(sfEntrySet{name: "after.txt", attrs: 0x20, cluster: skAfter, size: 4, noFatChain: true})

	return image
}

func openSkipImage(t *testing.T, image []byte) *libxfat.ExFAT {
	t.Helper()

	fs, err := libxfat.Open(libxfat.Source{
		Reader:                bytes.NewReader(image),
		Size:                  int64(len(image)),
		Strict:                true,
		IgnorePartitionOffset: true,
	})
	if err != nil {
		t.Fatalf("open the skip image: %v", err)
	}
	return fs
}

// TestWalkWithoutOnSkipIsUnchanged is both the additivity pin and the statement of
// the defect: with no callback, the empty directory and the four undescended ones
// are indistinguishable - each is reported with no children - and the walk still
// returns nil.
func TestWalkWithoutOnSkipIsUnchanged(t *testing.T) {
	fs := openSkipImage(t, buildSkipImage(t))

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
		"/empty",
		"/baddir",
		"/fardir",
		"/outer",
		"/outer/inner",
		"/outer/inner/loop",
		"/" + skBlankName,
		"/new",
		"/new/kept.txt",
		"/after.txt",
		"/$MBR",
		"/$FAT1",
		"/$OrphanFiles",
	}

	got := walkPaths(walkAll(t, fs, libxfat.WalkOptions{}))
	if !slices.Equal(got, want) {
		t.Fatalf("walk reported\n got %q\nwant %q", got, want)
	}
}

// TestWalkDeletedDirectoryDoesNotHideALiveReuse is the XFAT-U2 regression. /old is
// a deleted directory whose first cluster /new has since taken, which is what a
// volume looks like after a directory is removed and another created: the new
// record lands after the stale one and the allocator hands it the freed cluster.
//
// The walk used to mark a directory's cluster as walked before trying to read it,
// deleted or not, so /old claimed cluster 65 without ever being descended - the
// option was off, or the bitmap said the cluster belonged to something else - and
// /new, arriving second, was taken for a revisit. Its whole live subtree vanished
// from every walk that asked for deleted records.
func TestWalkDeletedDirectoryDoesNotHideALiveReuse(t *testing.T) {
	fs := openSkipImage(t, buildSkipImage(t))

	for _, opts := range []libxfat.WalkOptions{
		{},
		{IncludeDeleted: true},
		{IncludeDeleted: true, DescendDeletedDirectories: true},
	} {
		paths := walkPaths(walkAll(t, fs, opts))
		if !slices.Contains(paths, "/new/kept.txt") {
			t.Errorf("%+v: /new/kept.txt missing; the deleted /old hid the live directory reusing its cluster: %q",
				opts, paths)
		}
		if opts.IncludeDeleted && !slices.Contains(paths, "/old") {
			t.Errorf("%+v: the deleted /old itself was not reported: %q", opts, paths)
		}
	}

	// A deleted directory that is descended still claims its cluster, so a cycle
	// made of deleted records is reported once and not followed.
	counts := make(map[string]int)
	for _, path := range walkPaths(walkAll(t, fs, libxfat.WalkOptions{
		IncludeDeleted:            true,
		DescendDeletedDirectories: true,
	})) {
		counts[path]++
		if strings.HasPrefix(path, "/ghost/again/") {
			t.Fatalf("the deleted cycle was followed: %q", path)
		}
	}
	if got := counts["/ghost/again"]; got != 1 {
		t.Errorf("/ghost/again reported %d times, want 1", got)
	}
}

// skipRecord is one OnSkip invocation.
type skipRecord struct {
	path   string
	entry  libxfat.Entry
	reason libxfat.SkipReason
	err    error
}

// collectWalkAndSkips runs a walk whose OnSkip records every call. It also checks
// the ordering OnSkip promises: a skip names the entry the walk callback was handed
// immediately before it, so nothing is ever reported skipped that was not first
// reported.
func collectWalkAndSkips(t *testing.T, fs *libxfat.ExFAT, opts libxfat.WalkOptions) ([]walkRecord, []skipRecord) {
	t.Helper()

	var seen []walkRecord
	var skips []skipRecord
	opts.OnSkip = func(path string, entry libxfat.Entry, reason libxfat.SkipReason, err error) error {
		if len(seen) == 0 || seen[len(seen)-1].path != path {
			t.Errorf("OnSkip(%q) was not preceded by the walk callback for the same path", path)
		}
		skips = append(skips, skipRecord{path, entry, reason, err})
		return nil
	}
	err := fs.WalkWithOptions(context.Background(), opts,
		func(path string, parent uint32, entry libxfat.Entry) error {
			seen = append(seen, walkRecord{path, parent, entry})
			return nil
		})
	if err != nil {
		t.Fatalf("WalkWithOptions(%+v): %v", opts, err)
	}
	return seen, skips
}

func skipPaths(skips []skipRecord) []string {
	paths := make([]string, 0, len(skips))
	for _, s := range skips {
		paths = append(paths, string(s.reason)+" "+s.path)
	}
	return paths
}

func skipFor(t *testing.T, skips []skipRecord, want string) skipRecord {
	t.Helper()
	for _, s := range skips {
		if s.path == want {
			return s
		}
	}
	t.Fatalf("no skip reported for %q; skips were %q", want, skipPaths(skips))
	return skipRecord{}
}

func hasSkip(skips []skipRecord, path string) bool {
	return slices.ContainsFunc(skips, func(s skipRecord) bool { return s.path == path })
}

// TestWalkOnSkipReportsEveryUndescendedDirectory is the headline XFAT-U1 test:
// each live skip is reported once, with its own reason, and the empty directory -
// which is what all of them look like without the callback - is not reported at
// all.
func TestWalkOnSkipReportsEveryUndescendedDirectory(t *testing.T) {
	fs := openSkipImage(t, buildSkipImage(t))

	records, skips := collectWalkAndSkips(t, fs, libxfat.WalkOptions{})

	if len(skips) != 4 {
		t.Fatalf("got %d skips %q, want exactly 4", len(skips), skipPaths(skips))
	}

	bad := skipFor(t, skips, "/baddir")
	if bad.reason != libxfat.SkipReadError {
		t.Errorf("/baddir reason = %q, want %q", bad.reason, libxfat.SkipReadError)
	}
	if !errors.Is(bad.err, libxfat.ErrBadCluster) {
		t.Errorf("/baddir error = %v, want the read's own ErrBadCluster", bad.err)
	}

	far := skipFor(t, skips, "/fardir")
	if far.reason != libxfat.SkipInvalidCluster {
		t.Errorf("/fardir reason = %q, want %q", far.reason, libxfat.SkipInvalidCluster)
	}
	if far.err != nil {
		t.Errorf("/fardir carried an error %v; no read was attempted", far.err)
	}

	loop := skipFor(t, skips, "/outer/inner/loop")
	if loop.reason != libxfat.SkipRevisit {
		t.Errorf("the cycle's reason = %q, want %q", loop.reason, libxfat.SkipRevisit)
	}
	if loop.err != nil {
		t.Errorf("the cycle carried an error %v; no read was attempted", loop.err)
	}

	// ReadDir declines a directory whose name is blank and says nothing, which a
	// walk would otherwise pass off as an empty directory.
	blank := skipFor(t, skips, "/"+skBlankName)
	if blank.reason != libxfat.SkipReadError {
		t.Errorf("the blank-named directory's reason = %q, want %q", blank.reason, libxfat.SkipReadError)
	}
	if !errors.Is(blank.err, libxfat.ErrInvalidEntry) {
		t.Errorf("the blank-named directory's error = %v, want ErrInvalidEntry", blank.err)
	}

	for _, s := range skips {
		if !s.entry.IsDir() {
			t.Errorf("%s was reported skipped but is not a directory", s.path)
		}
		if !strings.HasSuffix(s.path, s.entry.Name()) {
			t.Errorf("skip path %q does not end in the entry's name %q", s.path, s.entry.Name())
		}
	}
	for _, path := range []string{"/empty", "/new"} {
		if hasSkip(skips, path) {
			t.Errorf("%s was reported skipped, but the walk descended it", path)
		}
	}

	// Every skipped directory was handed to the walk callback, and the walk
	// carried on to the end.
	paths := walkPaths(records)
	for _, want := range []string{"/empty", "/baddir", "/fardir", "/outer/inner/loop", "/" + skBlankName, "/after.txt", "/$OrphanFiles"} {
		if !slices.Contains(paths, want) {
			t.Errorf("%s missing from the walk; walked %q", want, paths)
		}
	}
}

func TestWalkOnSkipReportsDepthLimit(t *testing.T) {
	fs := openSkipImage(t, buildSkipImage(t))

	_, atTwo := collectWalkAndSkips(t, fs, libxfat.WalkOptions{MaxDepth: 2})
	inner := skipFor(t, atTwo, "/outer/inner")
	if inner.reason != libxfat.SkipDepthLimit {
		t.Errorf("/outer/inner reason = %q, want %q", inner.reason, libxfat.SkipDepthLimit)
	}
	if inner.err != nil {
		t.Errorf("/outer/inner carried an error %v; the depth cap is not a failure", inner.err)
	}

	// The depth check comes first, so a directory on the limit reports the cap
	// whatever else is true of it - including an empty one, which the walk did not
	// read and so cannot know is empty.
	_, atOne := collectWalkAndSkips(t, fs, libxfat.WalkOptions{MaxDepth: 1})
	for _, path := range []string{"/outer", "/fardir", "/empty"} {
		if s := skipFor(t, atOne, path); s.reason != libxfat.SkipDepthLimit {
			t.Errorf("%s at the depth limit reason = %q, want %q", path, s.reason, libxfat.SkipDepthLimit)
		}
	}
}

// TestWalkOnSkipReportsReallocatedDeletedDirectory covers the skip that is the
// deleted descent's own refusal rather than a limit or a failed read.
func TestWalkOnSkipReportsReallocatedDeletedDirectory(t *testing.T) {
	fs := openSkipImage(t, buildSkipImage(t))

	_, skips := collectWalkAndSkips(t, fs, libxfat.WalkOptions{
		IncludeDeleted:            true,
		DescendDeletedDirectories: true,
	})

	old := skipFor(t, skips, "/old")
	if old.reason != libxfat.SkipDeletedUnrecoverable {
		t.Errorf("/old reason = %q, want %q", old.reason, libxfat.SkipDeletedUnrecoverable)
	}
	if old.err != nil {
		t.Errorf("/old carried an error %v; the bitmap answered, nothing failed", old.err)
	}

	again := skipFor(t, skips, "/ghost/again")
	if again.reason != libxfat.SkipRevisit {
		t.Errorf("/ghost/again reason = %q, want %q", again.reason, libxfat.SkipRevisit)
	}

	for _, path := range []string{"/docs/gone", "/ghost", "/new"} {
		if hasSkip(skips, path) {
			t.Errorf("%s was reported skipped, but the walk descended it; skips %q", path, skipPaths(skips))
		}
	}

	// Not descending because the option is off is the caller's own choice, not
	// something the walk decided, so it is not reported.
	_, skips = collectWalkAndSkips(t, fs, libxfat.WalkOptions{IncludeDeleted: true})
	for _, path := range []string{"/old", "/ghost", "/docs/gone"} {
		if hasSkip(skips, path) {
			t.Errorf("%s was reported skipped without DescendDeletedDirectories; skips %q", path, skipPaths(skips))
		}
	}
}

func TestWalkOnSkipErrorStopsTheWalk(t *testing.T) {
	fs := openSkipImage(t, buildSkipImage(t))

	sentinel := errors.New("stop at the first skip")
	calls := 0
	var got []string
	err := fs.WalkWithOptions(context.Background(), libxfat.WalkOptions{
		OnSkip: func(string, libxfat.Entry, libxfat.SkipReason, error) error {
			calls++
			return sentinel
		},
	}, func(path string, _ uint32, _ libxfat.Entry) error {
		got = append(got, path)
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WalkWithOptions = %v, want OnSkip's own error", err)
	}
	if calls != 1 {
		t.Errorf("OnSkip ran %d times, want 1", calls)
	}
	// /baddir is the first skip in disk order; nothing may follow it.
	if last := got[len(got)-1]; last != "/baddir" {
		t.Errorf("the walk continued past the OnSkip error to %q; walked %q", last, got)
	}
}

// TestWalkStopOnReadErrorStillAborts pins the documented interaction: with
// StopOnReadError an unreadable directory aborts the walk as it always has, with or
// without OnSkip, and OnSkip hears nothing, because nothing was skipped.
func TestWalkStopOnReadErrorStillAborts(t *testing.T) {
	fs := openSkipImage(t, buildSkipImage(t))

	for _, withOnSkip := range []bool{false, true} {
		calls := 0
		opts := libxfat.WalkOptions{StopOnReadError: true}
		if withOnSkip {
			opts.OnSkip = func(string, libxfat.Entry, libxfat.SkipReason, error) error {
				calls++
				return nil
			}
		}

		var got []string
		err := fs.WalkWithOptions(context.Background(), opts,
			func(path string, _ uint32, _ libxfat.Entry) error {
				got = append(got, path)
				return nil
			})
		if !errors.Is(err, libxfat.ErrBadCluster) {
			t.Errorf("OnSkip set %v: WalkWithOptions = %v, want ErrBadCluster", withOnSkip, err)
		}
		if calls != 0 {
			t.Errorf("OnSkip ran %d times; an aborted walk skipped nothing", calls)
		}
		if last := got[len(got)-1]; last != "/baddir" {
			t.Errorf("OnSkip set %v: the walk continued past the abort to %q", withOnSkip, last)
		}
	}
}

// TestWalkOnSkipBlankNameDoesNotAbort pins the one read-error skip StopOnReadError
// does not turn into an abort. ReadDir has never treated a blank-named directory
// as a failure, so a walk never stopped on one; OnSkip adds the report without
// changing that.
func TestWalkOnSkipBlankNameDoesNotAbort(t *testing.T) {
	image := buildSkipImage(t)
	// Mend /baddir's chain, so the blank-named directory is the first skip.
	fat := sfFatOffsetSector * sfSectorSize
	binary.LittleEndian.PutUint32(image[fat+skBad*4:fat+skBad*4+4], 0xffffffff)
	fs := openSkipImage(t, image)

	records, skips := collectWalkAndSkips(t, fs, libxfat.WalkOptions{StopOnReadError: true})

	blank := skipFor(t, skips, "/"+skBlankName)
	if blank.reason != libxfat.SkipReadError || !errors.Is(blank.err, libxfat.ErrInvalidEntry) {
		t.Errorf("blank-named directory: reason %q, error %v; want %q wrapping ErrInvalidEntry",
			blank.reason, blank.err, libxfat.SkipReadError)
	}
	if !slices.Contains(walkPaths(records), "/after.txt") {
		t.Errorf("StopOnReadError aborted on the blank-named directory; walked %q", walkPaths(records))
	}
}
