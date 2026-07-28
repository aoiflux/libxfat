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

		if got := entry.GetSize(); got != 0 {
			t.Errorf("%s: GetSize() = %d, want 0 (no content stream)", name, got)
		}
		if got := entry.GetEntryCluster(); got != 0 {
			t.Errorf("%s: GetEntryCluster() = %d, want 0 (no allocation)", name, got)
		}
		if got := entry.GetValidDataSize(); got != 0 {
			t.Errorf("%s: GetValidDataSize() = %d, want 0", name, got)
		}
	}

	// The neighbouring records must still be reported correctly.
	upcase := findEntry(t, entries, "$UpCase")
	if upcase.GetSize() == 0 || upcase.GetEntryCluster() == 0 {
		t.Fatalf("$UpCase lost its own stream: size = %d, cluster = %d",
			upcase.GetSize(), upcase.GetEntryCluster())
	}
}

// TestGetClusterListOnRegionEntry pins the other half: region-backed entries
// have no cluster mapping, and previously produced an "invalid cluster" error
// naming a number derived from a first cluster of zero rather than saying so.
func TestGetClusterListOnRegionEntry(t *testing.T) {
	fs, entries := openTestVolume(t)

	for _, name := range []string{"$MBR", "$FAT1"} {
		entry := findEntry(t, entries, name)

		clusters, tail, err := fs.GetClusterList(entry)
		if !errors.Is(err, libxfat.ErrNoClusterMapping) {
			t.Errorf("%s: GetClusterList() error = %v, want ErrNoClusterMapping", name, err)
		}
		if clusters != nil || tail != 0 {
			t.Errorf("%s: GetClusterList() = (%v, %d), want (nil, 0)", name, clusters, tail)
		}

		offset, isRegion := entry.GetRegionOffset()
		if !isRegion {
			t.Errorf("%s: GetRegionOffset() reported not a region", name)
		}
		if entry.GetSize() == 0 {
			t.Errorf("%s: region has zero size", name)
		}

		// The offset and size must describe a range that is actually readable.
		data := testImageBytes()
		if offset+entry.GetSize() > uint64(len(data)) {
			t.Errorf("%s: region [%d, %d) falls outside a %d byte image",
				name, offset, offset+entry.GetSize(), len(data))
		}
	}
}

// The companion check - that an out-of-range first cluster is reported as
// itself rather than as a number derived from it - needs an Entry built with a
// cluster the validators would never let through, so it lives in
// cluster_mapping_internal_test.go.
