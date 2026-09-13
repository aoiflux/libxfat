package test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoiflux/libxfat/v2"
)

// The fixture's fragmented file: 8000 bytes laid out as cluster 12 then cluster
// 14, with 13 left as a gap. Derived here rather than hardcoded so that a change
// to the fixture's geometry shows up as a failing expectation, not a silent pass.
const (
	fgClusterSize  = sfClusterSize                     // 4096
	fgHeapStart    = sfHeapOffsetSector * sfSectorSize // 69632
	fgFirstOffset  = fgHeapStart + (sfFragmentCluster-2)*fgClusterSize
	fgSecondOffset = fgHeapStart + (sfFragmentCluster2-2)*fgClusterSize
	fgSize         = 8000
	fgSecondLen    = fgSize - fgClusterSize // 3904
	fgSlackLen     = fgClusterSize - fgSecondLen
)

// entryNamed locates one entry of the fixture by name.
func entryNamed(t *testing.T, fs *libxfat.ExFAT, name string) libxfat.Entry {
	t.Helper()

	root, err := fs.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir: %v", err)
	}
	all, err := fs.AllEntries(root)
	if err != nil {
		t.Fatalf("AllEntries: %v", err)
	}
	for _, e := range all {
		if e.Name() == name {
			return e
		}
	}
	t.Fatalf("entry %q not found in the fixture", name)
	return libxfat.Entry{}
}

// TestFragmentOffsetsLocatesBothRuns is the core extent assertion: a fragmented
// file yields one Range per run, in file order, with the last trimmed to the
// recorded size.
func TestFragmentOffsetsLocatesBothRuns(t *testing.T) {
	fs := openSuperfloppy(t, true)
	entry := entryNamed(t, fs, "fragmented.bin")

	ranges, err := fs.FragmentOffsets(entry)
	if err != nil {
		t.Fatalf("FragmentOffsets: %v", err)
	}

	if len(ranges) != 2 {
		t.Fatalf("FragmentOffsets returned %d ranges (%v), want 2", len(ranges), ranges)
	}
	if !libxfat.IsFragmented(ranges) {
		t.Fatal("IsFragmented = false for a file laid out across a gap")
	}

	first, second := ranges[0], ranges[1]
	if first.StartByte != fgFirstOffset || first.Length != fgClusterSize {
		t.Errorf("first run = [%d,+%d), want [%d,+%d)",
			first.StartByte, first.Length, fgFirstOffset, fgClusterSize)
	}
	if first.StartCluster != sfFragmentCluster || first.ClusterCount != 1 {
		t.Errorf("first run clusters = %d+%d, want %d+1",
			first.StartCluster, first.ClusterCount, sfFragmentCluster)
	}
	if second.StartByte != fgSecondOffset || second.Length != fgSecondLen {
		t.Errorf("second run = [%d,+%d), want [%d,+%d)",
			second.StartByte, second.Length, fgSecondOffset, fgSecondLen)
	}
	if second.StartCluster != sfFragmentCluster2 || second.ClusterCount != 1 {
		t.Errorf("second run clusters = %d+%d, want %d+1",
			second.StartCluster, second.ClusterCount, sfFragmentCluster2)
	}

	if total := libxfat.TotalLength(ranges); total != fgSize {
		t.Errorf("TotalLength = %d, want the recorded size %d", total, fgSize)
	}
	for _, r := range ranges {
		if r.Sparse {
			t.Error("a range is marked Sparse; exFAT has no sparse allocation")
		}
	}

	// Coalesce must not merge runs separated by a real gap on disk.
	if merged := libxfat.Coalesce(ranges); len(merged) != 2 {
		t.Errorf("Coalesce merged %d non-adjacent runs into %d", len(ranges), len(merged))
	}
}

// TestFragmentOffsetsAgreeWithClusterList cross-checks the new extent API against
// the long-standing cluster API. They derive the same layout by different routes,
// so a disagreement means one of them is wrong.
func TestFragmentOffsetsAgreeWithClusterList(t *testing.T) {
	fs := openSuperfloppy(t, true)
	entry := entryNamed(t, fs, "fragmented.bin")

	ranges, err := fs.FragmentOffsets(entry)
	if err != nil {
		t.Fatalf("FragmentOffsets: %v", err)
	}
	clusters, _, err := fs.ClusterList(entry)
	if err != nil {
		t.Fatalf("ClusterList: %v", err)
	}

	// Expand the ranges back into individual clusters and compare.
	var expanded []uint32
	for _, r := range ranges {
		for i := uint32(0); i < r.ClusterCount; i++ {
			expanded = append(expanded, r.StartCluster+i)
		}
	}
	if len(expanded) != len(clusters) {
		t.Fatalf("ranges expand to %v, ClusterList gives %v", expanded, clusters)
	}
	for i := range clusters {
		if expanded[i] != clusters[i] {
			t.Fatalf("ranges expand to %v, ClusterList gives %v", expanded, clusters)
		}
	}

	// And every range must agree with ClusterOffset for its first cluster.
	for _, r := range ranges {
		start, err := fs.ClusterOffset(r.StartCluster)
		if err != nil {
			t.Fatalf("ClusterOffset(%d): %v", r.StartCluster, err)
		}
		if want := int64(start); r.StartByte != want {
			t.Errorf("range at cluster %d starts at %d, ClusterOffset says %d",
				r.StartCluster, r.StartByte, want)
		}
	}
}

// TestFragmentOffsetsMatchExtractedBytes is the invariant that matters most: the
// extent API and the extraction path must describe the same bytes. If they ever
// disagree, one of them is lying about where a file lives.
func TestFragmentOffsetsMatchExtractedBytes(t *testing.T) {
	fs := openSuperfloppy(t, true)
	entry := entryNamed(t, fs, "fragmented.bin")

	ranges, err := fs.FragmentOffsets(entry)
	if err != nil {
		t.Fatalf("FragmentOffsets: %v", err)
	}

	// What the extraction path produces.
	dst := filepath.Join(t.TempDir(), "extracted.bin")
	if err := fs.ExtractEntryContent(entry, dst); err != nil {
		t.Fatalf("ExtractEntryContent: %v", err)
	}
	extracted, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read extracted file: %v", err)
	}

	// What the ranges point at, read straight out of the image.
	image := buildSuperfloppyImage()
	var located []byte
	for _, r := range ranges {
		located = append(located, image[r.StartByte:r.EndByte()]...)
	}

	if len(extracted) != len(located) {
		t.Fatalf("extraction produced %d bytes, the ranges describe %d",
			len(extracted), len(located))
	}
	for i := range located {
		if extracted[i] != located[i] {
			t.Fatalf("extraction and the ranges disagree at byte %d: %#x vs %#x",
				i, extracted[i], located[i])
		}
	}
	if len(located) != fgSize {
		t.Errorf("located %d bytes, want the recorded size %d", len(located), fgSize)
	}
}

// TestSlackRangeFindsTheClusterTail pins the slack region: the unused remainder
// of the last cluster, which is where the previous occupant's bytes survive.
func TestSlackRangeFindsTheClusterTail(t *testing.T) {
	fs := openSuperfloppy(t, true)
	entry := entryNamed(t, fs, "fragmented.bin")

	slack, ok, err := fs.SlackRange(entry)
	if err != nil {
		t.Fatalf("SlackRange: %v", err)
	}
	if !ok {
		t.Fatal("SlackRange reported none for a file ending mid-cluster")
	}
	if slack.Length != fgSlackLen {
		t.Errorf("slack length = %d, want %d", slack.Length, fgSlackLen)
	}
	if want := int64(fgSecondOffset + fgSecondLen); slack.StartByte != want {
		t.Errorf("slack starts at %d, want %d (just past the content)", slack.StartByte, want)
	}
	if slack.StartCluster != sfFragmentCluster2 {
		t.Errorf("slack cluster = %d, want %d", slack.StartCluster, sfFragmentCluster2)
	}
}

// TestFragmentOffsetsOnRegionEntries covers the deliberate asymmetry: the cluster
// API refuses a region because it has no clusters, while the extent API describes
// it, because a byte range is what it deals in.
func TestFragmentOffsetsOnRegionEntries(t *testing.T) {
	fs := openSuperfloppy(t, true)

	for _, name := range []string{"$MBR", "$FAT1"} {
		t.Run(name, func(t *testing.T) {
			entry := entryNamed(t, fs, name)

			if _, _, err := fs.ClusterList(entry); !errors.Is(err, libxfat.ErrNoClusterMapping) {
				t.Fatalf("ClusterList error = %v, want ErrNoClusterMapping", err)
			}

			ranges, err := fs.FragmentOffsets(entry)
			if err != nil {
				t.Fatalf("FragmentOffsets: %v", err)
			}
			if len(ranges) != 1 {
				t.Fatalf("FragmentOffsets gave %d ranges, want 1 for a region", len(ranges))
			}

			offset, isRegion := entry.RegionOffset()
			if !isRegion {
				t.Fatal("entry does not report itself as a region")
			}
			if ranges[0].StartByte != int64(offset) {
				t.Errorf("range starts at %d, region offset is %d", ranges[0].StartByte, offset)
			}
			if ranges[0].Length != int64(entry.Size()) {
				t.Errorf("range length = %d, entry size is %d", ranges[0].Length, entry.Size())
			}
			// A region is not cluster-addressed, and must not pretend to be.
			if ranges[0].StartCluster != 0 || ranges[0].ClusterCount != 0 {
				t.Errorf("region range claims clusters %d+%d, want 0+0",
					ranges[0].StartCluster, ranges[0].ClusterCount)
			}
		})
	}
}

// TestFragmentedContentIsInChainOrder pins that a fragmented file's bytes come back
// in chain order, from the clusters the chain actually names.
//
// fragmented.bin chains cluster 12 to cluster 14, skipping 13, and the two clusters
// carry different fill bytes. Comparing the read against the extraction - which
// TestReadEntryMatchesExtraction does - cannot catch a reader that follows the chain
// wrongly, because both paths would follow it wrongly together. This compares
// against what the builder wrote.
func TestFragmentedContentIsInChainOrder(t *testing.T) {
	fs := openSuperfloppy(t, true)

	entry := entryNamed(t, fs, "fragmented.bin")
	content, err := fs.ReadEntry(entry)
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}

	want := append(bytes.Repeat([]byte("F"), fgClusterSize),
		bytes.Repeat([]byte("f"), fgSecondLen)...)
	if !bytes.Equal(content, want) {
		t.Fatalf("fragmented.bin read %d bytes, want %d; first difference at %d",
			len(content), len(want), firstDifference(content, want))
	}

	// The gap is the point: cluster 13 lies physically between the two runs and
	// must not appear in the content, even though a reader that assumed contiguity
	// would find it there.
	gap, err := fs.ClusterOffset(sfFragmentCluster + 1)
	if err != nil {
		t.Fatalf("ClusterOffset(%d): %v", sfFragmentCluster+1, err)
	}
	ranges, err := fs.FragmentOffsets(entry)
	if err != nil {
		t.Fatalf("FragmentOffsets: %v", err)
	}
	for _, r := range ranges {
		if r.StartByte <= int64(gap) && int64(gap) < r.EndByte() {
			t.Errorf("range %s covers the skipped cluster at byte %d", r, gap)
		}
	}
}

// TestUnwrittenRangesDescribeTheNeverWrittenTail is the first test this API has
// had: until unwritten.bin existed, no fixture recorded a ValidDataLength short of
// its DataLength, so there was nothing for UnwrittenRanges to describe.
//
// unwritten.bin is 3000 bytes allocated with only the first 100 ever written.
func TestUnwrittenRangesDescribeTheNeverWrittenTail(t *testing.T) {
	fs := openSuperfloppy(t, true)
	entry := entryNamed(t, fs, "unwritten.bin")

	const size, valid = 3000, 100

	if got := entry.Size(); got != size {
		t.Fatalf("Size() = %d, want %d", got, size)
	}
	if got := entry.ValidDataSize(); got != valid {
		t.Fatalf("ValidDataSize() = %d, want %d", got, valid)
	}

	result, err := fs.FragmentOffsetsWithOptions(entry, libxfat.FragmentOptions{})
	if err != nil {
		t.Fatalf("FragmentOffsetsWithOptions: %v", err)
	}

	// The main range list stays gap-free and sums to the recorded size. Splitting
	// it at the valid-data boundary would manufacture a second Range over
	// physically contiguous bytes, and make a contiguous file report as
	// fragmented because of a number in its directory entry.
	if total := libxfat.TotalLength(result.Ranges); total != size {
		t.Errorf("ranges cover %d bytes, want %d", total, size)
	}
	if len(result.Ranges) != 1 {
		t.Errorf("got %d ranges for a contiguous file, want 1: %v", len(result.Ranges), result.Ranges)
	}
	if result.ValidBytes != valid {
		t.Errorf("ValidBytes = %d, want %d", result.ValidBytes, valid)
	}

	unwritten, err := fs.UnwrittenRanges(entry)
	if err != nil {
		t.Fatalf("UnwrittenRanges: %v", err)
	}
	if total := libxfat.TotalLength(unwritten); total != size-valid {
		t.Errorf("unwritten ranges cover %d bytes, want %d", total, size-valid)
	}
	if len(unwritten) > 0 {
		if got, want := unwritten[0].StartByte, result.Ranges[0].StartByte+valid; got != want {
			t.Errorf("unwritten region starts at %d, want %d", got, want)
		}
	}

	// A file written in full has no unwritten region at all.
	full, err := fs.UnwrittenRanges(entryNamed(t, fs, "readme.txt"))
	if err != nil {
		t.Fatalf("UnwrittenRanges(readme.txt): %v", err)
	}
	if len(full) != 0 {
		t.Errorf("a fully written file reports %d unwritten ranges: %v", len(full), full)
	}
}

// TestThreeRunFileCoalescesCorrectly uses three runs because two cannot tell
// correct coalescing from one-run-per-cluster luck: with two clusters, "emit a run
// per cluster" and "emit a run per contiguous group" produce the same answer.
//
// threerun.bin chains 22 -> 24 -> 26, so its three single-cluster runs have gaps
// between all of them, and its recorded size trims the last.
func TestThreeRunFileCoalescesCorrectly(t *testing.T) {
	fs := openSuperfloppy(t, true)
	entry := entryNamed(t, fs, "threerun.bin")

	const size = 10000

	ranges, err := fs.FragmentOffsets(entry)
	if err != nil {
		t.Fatalf("FragmentOffsets: %v", err)
	}
	if len(ranges) != 3 {
		t.Fatalf("got %d ranges, want 3: %v", len(ranges), ranges)
	}
	if total := libxfat.TotalLength(ranges); total != size {
		t.Errorf("ranges cover %d bytes, want %d", total, size)
	}

	for i, cluster := range []uint32{sfThreeRunA, sfThreeRunB, sfThreeRunC} {
		want, err := fs.ClusterOffset(cluster)
		if err != nil {
			t.Fatalf("ClusterOffset(%d): %v", cluster, err)
		}
		if ranges[i].StartByte != int64(want) {
			t.Errorf("range %d starts at %d, cluster %d is at %d",
				i, ranges[i].StartByte, cluster, want)
		}
		if ranges[i].ClusterCount != 1 {
			t.Errorf("range %d spans %d clusters, want 1", i, ranges[i].ClusterCount)
		}
	}

	// The last run is trimmed to the recorded size, the others are whole clusters.
	if got := ranges[2].Length; got != size-2*sfClusterSize {
		t.Errorf("final range length = %d, want %d", got, size-2*sfClusterSize)
	}

	// Nothing to merge: the runs are not adjacent.
	if merged := libxfat.Coalesce(ranges); len(merged) != 3 {
		t.Errorf("Coalesce merged non-adjacent runs into %d: %v", len(merged), merged)
	}

	// And the content arrives run by run, in chain order.
	content, err := fs.ReadEntry(entry)
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}
	want := bytes.Repeat([]byte("1"), sfClusterSize)
	want = append(want, bytes.Repeat([]byte("2"), sfClusterSize)...)
	want = append(want, bytes.Repeat([]byte("3"), size-2*sfClusterSize)...)
	if !bytes.Equal(content, want) {
		t.Errorf("content differs from the fixture at byte %d", firstDifference(content, want))
	}
}

// TestDescendingChainIsTwoRuns pins the run-boundary test. A chain that goes
// backwards - 30 then 29 - is two runs, and an implementation that asks "is this
// cluster different from the last one" rather than "is it the one after it" merges
// the pair into a single run whose length runs backwards from its start.
func TestDescendingChainIsTwoRuns(t *testing.T) {
	fs := openSuperfloppy(t, true)
	entry := entryNamed(t, fs, "descending.bin")

	ranges, err := fs.FragmentOffsets(entry)
	if err != nil {
		t.Fatalf("FragmentOffsets: %v", err)
	}
	if len(ranges) != 2 {
		t.Fatalf("got %d ranges for a descending chain, want 2: %v", len(ranges), ranges)
	}

	// The second range is physically before the first, which is legal and is the
	// point: a Range list is in chain order, not offset order.
	if !(ranges[1].StartByte < ranges[0].StartByte) {
		t.Errorf("expected the second range to lie before the first: %v", ranges)
	}
	for i, r := range ranges {
		if r.Length <= 0 {
			t.Errorf("range %d has length %d", i, r.Length)
		}
		if r.EndByte() != r.StartByte+r.Length {
			t.Errorf("range %d: EndByte %d does not follow from start %d and length %d",
				i, r.EndByte(), r.StartByte, r.Length)
		}
	}
	if total := libxfat.TotalLength(ranges); total != 5000 {
		t.Errorf("ranges cover %d bytes, want 5000", total)
	}

	content, err := fs.ReadEntry(entry)
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}
	want := append(bytes.Repeat([]byte("H"), sfClusterSize),
		bytes.Repeat([]byte("L"), 5000-sfClusterSize)...)
	if !bytes.Equal(content, want) {
		t.Errorf("content differs from the fixture at byte %d", firstDifference(content, want))
	}
}
