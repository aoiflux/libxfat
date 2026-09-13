package test

import (
	"errors"
	"testing"

	"github.com/aoiflux/libxfat"
)

// TestGuidTexfatActCarryNoStream pins a stale-state bug: $Volume GUID, $TexFAT
// and $ACT records carry no cluster or length of their own, but were being
// emitted by patching a shared Entry that still held the cluster and size of
// whichever $BitMap or $UpCase record was parsed immediately before them. They
// therefore reported the up-case table's size and cluster as their own.
func TestGuidTexfatActCarryNoStream(t *testing.T) {
	_, entries := openTestVolume(t)

	// The test image lays out $BitMap and $UpCase before these three, so any
	// leakage shows up here.
	for _, name := range []string{"$Volume GUID", "$TexFAT", "$ACT"} {
		entry := findEntry(t, entries, name)

		if got := entry.Size(); got != 0 {
			t.Errorf("%s: Size() = %d, want 0 (no content stream)", name, got)
		}
		if got := entry.FirstCluster(); got != 0 {
			t.Errorf("%s: FirstCluster() = %d, want 0 (no allocation)", name, got)
		}
		if got := entry.ValidDataSize(); got != 0 {
			t.Errorf("%s: ValidDataSize() = %d, want 0", name, got)
		}
	}

	// The neighbouring records must still be reported correctly.
	upcase := findEntry(t, entries, "$UpCase")
	if upcase.Size() == 0 || upcase.FirstCluster() == 0 {
		t.Fatalf("$UpCase lost its own stream: size = %d, cluster = %d",
			upcase.Size(), upcase.FirstCluster())
	}
}

// TestGetClusterListOnRegionEntry pins the other half: region-backed entries
// have no cluster mapping, and previously produced an "invalid cluster" error
// naming a number derived from a first cluster of zero rather than saying so.
func TestGetClusterListOnRegionEntry(t *testing.T) {
	fs, entries := openTestVolume(t)

	for _, name := range []string{"$MBR", "$FAT1"} {
		entry := findEntry(t, entries, name)

		clusters, tail, err := fs.ClusterList(entry)
		if !errors.Is(err, libxfat.ErrNoClusterMapping) {
			t.Errorf("%s: ClusterList() error = %v, want ErrNoClusterMapping", name, err)
		}
		if clusters != nil || tail != 0 {
			t.Errorf("%s: ClusterList() = (%v, %d), want (nil, 0)", name, clusters, tail)
		}

		offset, isRegion := entry.RegionOffset()
		if !isRegion {
			t.Errorf("%s: RegionOffset() reported not a region", name)
		}
		if entry.Size() == 0 {
			t.Errorf("%s: region has zero size", name)
		}

		// The offset and size must describe a range that is actually readable.
		data := testImageBytes()
		if offset+entry.Size() > uint64(len(data)) {
			t.Errorf("%s: region [%d, %d) falls outside a %d byte image",
				name, offset, offset+entry.Size(), len(data))
		}
	}
}

// The companion check - that an out-of-range first cluster is reported as
// itself rather than as a number derived from it - needs an Entry built with a
// cluster the validators would never let through, so it lives in
// cluster_mapping_internal_test.go.
