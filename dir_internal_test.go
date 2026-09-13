package libxfat

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestGetAllocatedClustersWithoutBitmapFails(t *testing.T) {
	exfat := ExFAT{}

	_, err := exfat.AllocatedClusters()
	if !errors.Is(err, ErrAllocationBitmapNotFound) {
		t.Fatalf("AllocatedClusters() error = %v, want ErrAllocationBitmapNotFound", err)
	}
}

// TestExtractionTargetStaysBeneathTheDestination is the guard against a crafted
// image writing outside the output directory.
//
// A directory or file name on an exFAT volume is whatever its name records say, so
// ".." is a name a hostile image can record, and joining it onto an output
// directory climbs out. The names are still reported by Walk; what is refused is
// following them out of the destination.
func TestExtractionTargetStaysBeneathTheDestination(t *testing.T) {
	dst := filepath.Join(string(filepath.Separator), "out")

	for _, tc := range []struct {
		path string
		want string // "" means the path must be refused
	}{
		{"/child.txt", filepath.Join(dst, "child.txt")},
		{"/nested/dir/child.txt", filepath.Join(dst, "nested", "dir", "child.txt")},
		{"/nested/dir/", filepath.Join(dst, "nested", "dir")},

		// A name that is literally "..", at every position it can appear in.
		{"/../escaped.txt", ""},
		{"/../../escaped.txt", ""},
		{"/nested/../../escaped.txt", ""},
		{"/..", ""},

		// The root itself, and anything that resolves to the destination, is not a
		// file to write.
		{"/", ""},
		{"", ""},
	} {
		got, ok := extractionTarget(dst, tc.path)
		if tc.want == "" {
			if ok {
				t.Errorf("extractionTarget(%q) = %q, want refusal", tc.path, got)
			}
			continue
		}
		if !ok {
			t.Errorf("extractionTarget(%q) was refused, want %q", tc.path, tc.want)
			continue
		}
		if got != tc.want {
			t.Errorf("extractionTarget(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestExtractionTargetKeepsInteriorDotDotNames checks that a "..", once collapsed
// by Clean, cannot cancel out a legitimate directory and land somewhere the tree
// does not describe. "/a/../b.txt" would extract as "b.txt" directly under the
// destination, which is not where the volume says the file is.
func TestExtractionTargetKeepsInteriorDotDotNames(t *testing.T) {
	dst := filepath.Join(string(filepath.Separator), "out")

	got, ok := extractionTarget(dst, "/a/../b.txt")
	if !ok {
		t.Fatal("extractionTarget refused a path that stays inside the destination")
	}
	if want := filepath.Join(dst, "b.txt"); got != want {
		t.Errorf("extractionTarget = %q, want %q", got, want)
	}
}
