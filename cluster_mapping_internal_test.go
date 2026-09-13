package libxfat

import (
	"errors"
	"strings"
	"testing"
)

// TestGetClusterListReportsTheBadCluster checks that an entry claiming an
// out-of-range first cluster is reported as itself. The arithmetic used to run
// first, so a first cluster of 0 on a 12-cluster file produced "invalid
// cluster: 11" - a number that appears nowhere in the image and sends whoever
// reads the error looking in the wrong place.
func TestGetClusterListReportsTheBadCluster(t *testing.T) {
	vbr := VBR{
		clusterSize: 512,
		nbClusters:  4,
	}

	cases := map[string]uint32{
		"zero first cluster": 0,
		"cluster one":        1,
		"past the heap":      0x00FF00FF,
	}

	for name, cluster := range cases {
		t.Run(name, func(t *testing.T) {
			entry := Entry{
				etype:          EXFAT_DIRRECORD_FILEDIR,
				dataLen:        6144, // twelve clusters
				entryCluster:   cluster,
				secondaryFlags: ALLOCATION_POSSIBLE_FLAG | NOT_FAT_CHAIN_FLAG,
			}

			_, _, err := vbr.getClusterList(entry)
			if !errors.Is(err, ErrInvalidCluster) {
				t.Fatalf("getClusterList() error = %v, want ErrInvalidCluster", err)
			}

			// The message must name the cluster the entry actually claims.
			want := itoa(cluster)
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("getClusterList() error = %q, want it to name cluster %s", err, want)
			}
			if cluster == 0 && strings.Contains(err.Error(), "11") {
				t.Fatalf("getClusterList() error = %q, still names a derived cluster", err)
			}
		})
	}
}

// TestGetClusterListRejectsRegionEntries covers the region case, where there is
// no cluster mapping to report at all.
func TestGetClusterListRejectsRegionEntries(t *testing.T) {
	vbr := VBR{clusterSize: 512, nbClusters: 4}
	entry := Entry{
		name:           FAT1,
		dataLen:        512,
		isRegion:       true,
		regionOffset:   6144,
		secondaryFlags: ALLOCATION_POSSIBLE_FLAG | NOT_FAT_CHAIN_FLAG,
	}

	clusters, tail, err := vbr.getClusterList(entry)
	if !errors.Is(err, ErrNoClusterMapping) {
		t.Fatalf("getClusterList() error = %v, want ErrNoClusterMapping", err)
	}
	if clusters != nil || tail != 0 {
		t.Fatalf("getClusterList() = (%v, %d), want (nil, 0)", clusters, tail)
	}

	offset, isRegion := entry.RegionOffset()
	if !isRegion || offset != 6144 {
		t.Fatalf("RegionOffset() = (%d, %v), want (6144, true)", offset, isRegion)
	}
}

func itoa(n uint32) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
