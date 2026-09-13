package test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoiflux/libxfat"
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
	all, err := fs.GetAllEntries(root)
	if err != nil {
		t.Fatalf("GetAllEntries: %v", err)
	}
	for _, e := range all {
		if e.GetName() == name {
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
	clusters, _, err := fs.GetClusterList(entry)
	if err != nil {
		t.Fatalf("GetClusterList: %v", err)
	}

	// Expand the ranges back into individual clusters and compare.
	var expanded []uint32
	for _, r := range ranges {
		for i := uint32(0); i < r.ClusterCount; i++ {
			expanded = append(expanded, r.StartCluster+i)
		}
	}
	if len(expanded) != len(clusters) {
		t.Fatalf("ranges expand to %v, GetClusterList gives %v", expanded, clusters)
	}
	for i := range clusters {
		if expanded[i] != clusters[i] {
			t.Fatalf("ranges expand to %v, GetClusterList gives %v", expanded, clusters)
		}
	}

	// And every range must agree with GetClusterOffset for its first cluster.
	for _, r := range ranges {
		if want := int64(fs.GetClusterOffset(r.StartCluster)); r.StartByte != want {
			t.Errorf("range at cluster %d starts at %d, GetClusterOffset says %d",
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

			if _, _, err := fs.GetClusterList(entry); !errors.Is(err, libxfat.ErrNoClusterMapping) {
				t.Fatalf("GetClusterList error = %v, want ErrNoClusterMapping", err)
			}

			ranges, err := fs.FragmentOffsets(entry)
			if err != nil {
				t.Fatalf("FragmentOffsets: %v", err)
			}
			if len(ranges) != 1 {
				t.Fatalf("FragmentOffsets gave %d ranges, want 1 for a region", len(ranges))
			}

			offset, isRegion := entry.GetRegionOffset()
			if !isRegion {
				t.Fatal("entry does not report itself as a region")
			}
			if ranges[0].StartByte != int64(offset) {
				t.Errorf("range starts at %d, region offset is %d", ranges[0].StartByte, offset)
			}
			if ranges[0].Length != int64(entry.GetSize()) {
				t.Errorf("range length = %d, entry size is %d", ranges[0].Length, entry.GetSize())
			}
			// A region is not cluster-addressed, and must not pretend to be.
			if ranges[0].StartCluster != 0 || ranges[0].ClusterCount != 0 {
				t.Errorf("region range claims clusters %d+%d, want 0+0",
					ranges[0].StartCluster, ranges[0].ClusterCount)
			}
		})
	}
}
