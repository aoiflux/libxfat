package test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/aoiflux/libxfat"
)

// TestVolumeAccessorsMatchTheBootSector checks every number the volume reports
// about itself against what the fixture builder wrote. All of these were parsed
// and then unreachable before v1.3.0, so a consumer wanting the sector size
// had to re-parse the boot sector this library had already read.
func TestVolumeAccessorsMatchTheBootSector(t *testing.T) {
	fs := openSuperfloppy(t, true)

	for _, tc := range []struct {
		name      string
		got, want uint64
	}{
		{"VolumeSerialNumber", uint64(fs.VolumeSerialNumber()), sfSerialNumber},
		{"BytesPerSector", uint64(fs.BytesPerSector()), sfSectorSize},
		{"SectorsPerCluster", uint64(fs.SectorsPerCluster()), sfSectorsPerClust},
		{"ClusterSize", fs.ClusterSize(), sfClusterSize},
		{"ClusterCount", uint64(fs.ClusterCount()), sfClusterCount},
		{"RootDirCluster", uint64(fs.RootDirCluster()), sfRootCluster},
		{"VolumeSize", fs.VolumeSize(), sfVolumeSectors},
		{"FatCount", uint64(fs.FatCount()), 1},
		{"PercentInUse", uint64(fs.PercentInUse()), 40},
		{"PartitionOffset", fs.PartitionOffset(), sfClaimedPartitionLBA},
		{"Base", uint64(fs.Base()), 0},
		{"FatOffset", uint64(fs.FatOffset()), sfFatOffsetSector * sfSectorSize},
		{"FatSize", fs.FatSize(), sfFatSizeSectors * sfSectorSize},
		{"ClusterHeapOffset", uint64(fs.ClusterHeapOffset()), sfHeapOffsetSector * sfSectorSize},
	} {
		if tc.got != tc.want {
			t.Errorf("%s() = %d, want %d", tc.name, tc.got, tc.want)
		}
	}

	if major, minor := fs.FilesystemRevision(); major != 1 || minor != 0 {
		t.Errorf("FilesystemRevision() = %d.%d, want 1.0", major, minor)
	}

	// Base is what every offset the library returns is measured from, so the
	// cluster heap offset must be the offset of cluster 2 and nothing else.
	cluster2, err := fs.ClusterOffset(uint32(libxfat.FIRST_CLUSTER_NUMBER))
	if err != nil {
		t.Fatalf("ClusterOffset(2): %v", err)
	}
	if int64(cluster2) != fs.ClusterHeapOffset() {
		t.Errorf("cluster 2 is at %d, ClusterHeapOffset() says %d",
			cluster2, fs.ClusterHeapOffset())
	}
}

// TestVolumeFlagsAreClean pins the decoding of a zeroed VolumeFlags field, which is
// what a cleanly unmounted single-FAT volume looks like.
func TestVolumeFlagsAreClean(t *testing.T) {
	fs := openSuperfloppy(t, true)

	if got := fs.VolumeFlags(); got != 0 {
		t.Errorf("VolumeFlags() = 0x%04x, want 0", got)
	}
	if fs.VolumeDirty() {
		t.Error("VolumeDirty() is true on a fixture whose flags are zero")
	}
	if fs.MediaFailure() {
		t.Error("MediaFailure() is true on a fixture whose flags are zero")
	}
	if got := fs.ActiveFAT(); got != 0 {
		t.Errorf("ActiveFAT() = %d, want 0", got)
	}
}

// TestVolumeFlagsDecodeEachBit patches the field rather than asserting against a
// fixture that is clean by design. VolumeDirty is the one flag here with
// investigative weight - it says the volume was not cleanly unmounted - so a
// library that parsed the field into the wrong bit would be quietly wrong about
// exactly the thing worth reporting.
func TestVolumeFlagsDecodeEachBit(t *testing.T) {
	for _, tc := range []struct {
		name        string
		flags       uint16
		activeFAT   int
		dirty, fail bool
	}{
		{"active FAT", 0x0001, 1, false, false},
		{"dirty", 0x0002, 0, true, false},
		{"media failure", 0x0004, 0, false, true},
		{"dirty and failed", 0x0006, 0, true, true},
		{"all set", 0x000f, 1, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			image := buildSuperfloppyImage()
			binary.LittleEndian.PutUint16(image[0x6a:0x6c], tc.flags)

			fs, err := libxfat.Open(libxfat.Source{
				Reader:                bytes.NewReader(image),
				Size:                  int64(len(image)),
				IgnorePartitionOffset: true,
			})
			if err != nil {
				t.Fatalf("open: %v", err)
			}

			if got := fs.VolumeFlags(); got != tc.flags {
				t.Errorf("VolumeFlags() = 0x%04x, want 0x%04x", got, tc.flags)
			}
			if got := fs.ActiveFAT(); got != tc.activeFAT {
				t.Errorf("ActiveFAT() = %d, want %d", got, tc.activeFAT)
			}
			if got := fs.VolumeDirty(); got != tc.dirty {
				t.Errorf("VolumeDirty() = %v, want %v", got, tc.dirty)
			}
			if got := fs.MediaFailure(); got != tc.fail {
				t.Errorf("MediaFailure() = %v, want %v", got, tc.fail)
			}
		})
	}
}

// TestVolumeLabelDoesNotDependOnCallOrder is the fix for the trap GetVolumeLabel
// carried: it returned the empty string until something else had happened to read
// the root directory, so the same call gave two different answers on the same
// volume and neither said it was incomplete.
func TestVolumeLabelDoesNotDependOnCallOrder(t *testing.T) {
	// Straight after opening, with nothing else called.
	label, err := openSuperfloppy(t, true).VolumeLabel()
	if err != nil {
		t.Fatalf("VolumeLabel() on a freshly opened volume: %v", err)
	}
	if label != sfVolumeLabel {
		t.Errorf("VolumeLabel() = %q, want %q", label, sfVolumeLabel)
	}

	// And after a root read, which is the only order that used to work.
	fs := openSuperfloppy(t, true)
	if _, err := fs.ReadRootDir(); err != nil {
		t.Fatalf("ReadRootDir: %v", err)
	}
	again, err := fs.VolumeLabel()
	if err != nil {
		t.Fatalf("VolumeLabel() after ReadRootDir: %v", err)
	}
	if again != label {
		t.Errorf("VolumeLabel() = %q before a root read and %q after", label, again)
	}
}
