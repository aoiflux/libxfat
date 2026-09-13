package test

import (
	"bytes"
	"context"
	"testing"

	"github.com/aoiflux/libxfat"
)

// TestChangeDetectionShape is the end-to-end acceptance test for what this whole
// API exists to serve: a consumer that knows which byte ranges of an image were
// written, and wants to know which files those writes landed in, without reading
// any file content.
//
// It is deliberately assembled the way that consumer would assemble it - Open over
// an io.ReaderAt with IgnorePartitionOffset, one Walk, FragmentOffsets per entry -
// so that a change which breaks the combination is caught here even if each piece
// still passes its own test.
func TestChangeDetectionShape(t *testing.T) {
	image := buildSuperfloppyImage()

	fs, err := libxfat.Open(libxfat.Source{
		Reader:                bytes.NewReader(image),
		Size:                  int64(len(image)),
		Strict:                true,
		IgnorePartitionOffset: true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// The changed ranges. Each is a single byte inside a cluster whose owner is
	// known from the fixture, so the expected answer is not in doubt. The second
	// one is in cluster 14, the far run of the fragmented file: an intersection
	// built on first-cluster-plus-size would miss it entirely, and an
	// implementation assuming contiguity would attribute it to whatever owns
	// cluster 13.
	type change struct {
		label string
		at    uint32 // cluster
		want  string // the path expected to contain it
	}
	changes := []change{
		{"readme's only cluster", sfReadmeCluster, "/readme.txt"},
		{"the far run of a fragmented file", sfFragmentCluster2, "/fragmented.bin"},
		{"the second run of a descending chain", sfDescendLow, "/descending.bin"},
		{"the middle run of a three-run file", sfThreeRunB, "/threerun.bin"},
		{"a nested directory's file", sfBuriedCluster, "/docs/" + sfUnnamedDirName + "/buried.txt"},
	}

	// Turn each into an absolute byte offset using the volume's own geometry, which
	// is what a consumer would have to do and could not before v1.3.0.
	offsets := make([]int64, len(changes))
	for i, c := range changes {
		base, err := fs.ClusterOffset(c.at)
		if err != nil {
			t.Fatalf("%s: ClusterOffset(%d): %v", c.label, c.at, err)
		}
		offsets[i] = int64(base) + 8 // a byte inside the cluster, not at its edge
	}

	// One pass. No file content is read: FragmentOffsets reads FAT entries only.
	hits := make(map[int]string, len(changes))
	err = fs.Walk(context.Background(), func(path string, _ uint32, entry libxfat.Entry) error {
		if entry.IsDir() {
			return nil
		}

		ranges, err := fs.FragmentOffsets(entry)
		if err != nil {
			// An entry with nothing to locate ($OrphanFiles) is not a failure.
			return nil
		}

		for i, offset := range offsets {
			for _, r := range ranges {
				if r.StartByte <= offset && offset < r.EndByte() {
					if previous, clash := hits[i]; clash {
						t.Errorf("byte %d claimed by both %q and %q", offset, previous, path)
					}
					hits[i] = path
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	for i, c := range changes {
		got, found := hits[i]
		if !found {
			t.Errorf("%s: byte %d was not attributed to any file, want %q",
				c.label, offsets[i], c.want)
			continue
		}
		if got != c.want {
			t.Errorf("%s: byte %d attributed to %q, want %q",
				c.label, offsets[i], got, c.want)
		}
	}
}

// TestChangeDetectionIdentifiesFilesAcrossTwoReads checks the other half of what a
// change-detection consumer needs: that a file recognised in one pass can be
// recognised again in a second, which is what FileID is for.
func TestChangeDetectionIdentifiesFilesAcrossTwoReads(t *testing.T) {
	image := buildSuperfloppyImage()

	open := func() *libxfat.ExFAT {
		t.Helper()
		fs, err := libxfat.Open(libxfat.Source{
			Reader:                bytes.NewReader(image),
			Size:                  int64(len(image)),
			IgnorePartitionOffset: true,
		})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		return fs
	}

	collect := func(fs *libxfat.ExFAT) map[libxfat.FileID]string {
		t.Helper()
		byID := make(map[libxfat.FileID]string)
		err := fs.Walk(context.Background(), func(path string, _ uint32, entry libxfat.Entry) error {
			if id, ok := fs.FileID(entry); ok {
				byID[id] = path
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Walk: %v", err)
		}
		return byID
	}

	before, after := collect(open()), collect(open())

	if len(before) == 0 {
		t.Fatal("no identifiable entries")
	}
	if len(before) != len(after) {
		t.Fatalf("two passes over the same image identified %d and %d entries",
			len(before), len(after))
	}
	for id, path := range before {
		if other, ok := after[id]; !ok {
			t.Errorf("%s (%s) has no counterpart in the second pass", path, id)
		} else if other != path {
			t.Errorf("%s names %q in one pass and %q in the other", id, path, other)
		}
	}
}
