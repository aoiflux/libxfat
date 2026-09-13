package test

import (
	"encoding/binary"
	"testing"

	"github.com/aoiflux/libxfat/v2"
)

// sfRecordOffset is where the fixture builder put slot number slot of the
// directory whose first cluster is cluster. The library is expected to arrive at
// the same number from the other direction, by walking the volume.
func sfRecordOffset(cluster uint32, slot uint32) int64 {
	heap := int64(sfHeapOffsetSector) * sfSectorSize
	return heap + int64(cluster-2)*sfClusterSize + int64(slot)*32
}

// dirOf reads one directory by name from the entries of its parent.
func dirOf(t *testing.T, fs *libxfat.ExFAT, parent []libxfat.Entry, name string) []libxfat.Entry {
	t.Helper()

	for _, entry := range parent {
		if entry.Name() != name {
			continue
		}
		children, err := fs.ReadDir(entry)
		if err != nil {
			t.Fatalf("ReadDir(%q): %v", name, err)
		}
		return children
	}
	t.Fatalf("directory %q not found among %v", name, sortedNames(parent))
	return nil
}

// TestIdentityOffsetAddressesItsOwnRecord is the load-bearing test for X4: it
// reads the image at the offset the library reports and checks that what is there
// really is this entry's primary record.
//
// Asserting the type byte alone would pass for any of the directory's other
// entry sets, so the recorded entry-set checksum is compared too. That value is
// derived from the whole set's bytes, so an offset that names the wrong set by
// even one slot fails.
func TestIdentityOffsetAddressesItsOwnRecord(t *testing.T) {
	image := buildSuperfloppyImage()
	fs := openSuperfloppy(t, true)

	checked := 0
	for _, entry := range collectAll(t, fs) {
		offset, ok := entry.EntrySetOffset()
		if !ok {
			continue
		}
		if offset < 0 || offset+32 > int64(len(image)) {
			t.Fatalf("%q: offset %d is outside the %d-byte image",
				entry.Name(), offset, len(image))
		}

		record := image[offset : offset+32]
		if record[0] != entry.EntryType() {
			t.Errorf("%q: record at offset %d has type 0x%02x, entry says 0x%02x",
				entry.Name(), offset, record[0], entry.EntryType())
			continue
		}

		// Only a file entry set carries a checksum at bytes 2..3; $BitMap and
		// $UpCase records use those bytes for something else.
		if record[0]&0x7f != 0x05 {
			checked++
			continue
		}
		expected, _, _ := entry.EntrySetChecksums()
		if got := binary.LittleEndian.Uint16(record[2:4]); got != expected {
			t.Errorf("%q: record at offset %d records checksum 0x%04x, entry expects 0x%04x",
				entry.Name(), offset, got, expected)
		}
		checked++
	}

	if checked == 0 {
		t.Fatal("no entry reported an entry-set offset; the address is not being recorded")
	}
	t.Logf("%d entries verified against their own records", checked)
}

// TestIdentityOffsetAgreesWithSlotIndex ties the physical address and the logical
// slot together. Either one alone can be off by a record without the other
// noticing, which is exactly the mistake this pair of fields invites.
func TestIdentityOffsetAgreesWithSlotIndex(t *testing.T) {
	fs := openSuperfloppy(t, true)

	for _, entry := range collectAll(t, fs) {
		offset, ok := entry.EntrySetOffset()
		if !ok {
			continue
		}
		want := sfRecordOffset(entry.ParentFirstCluster(), entry.EntrySlotIndex())
		if offset != want {
			t.Errorf("%q: reports offset %d, but parent cluster %d slot %d is at %d",
				entry.Name(), offset, entry.ParentFirstCluster(),
				entry.EntrySlotIndex(), want)
		}
	}
}

// TestIdentityParentNamesTheDirectory checks that an entry's parent cluster is
// the directory it was actually read from, at each level of the tree. The root is
// included deliberately: exFAT's root is a real cluster chain, so entries under
// it must carry a real cluster and not a sentinel.
func TestIdentityParentNamesTheDirectory(t *testing.T) {
	fs := openSuperfloppy(t, true)

	root, err := fs.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir: %v", err)
	}

	assertParent := func(label string, want uint32, entries []libxfat.Entry) {
		t.Helper()
		seen := 0
		for _, entry := range entries {
			if _, ok := entry.EntrySetOffset(); !ok {
				continue // a synthetic entry with no record of its own
			}
			if got := entry.ParentFirstCluster(); got != want {
				t.Errorf("%s: %q reports parent cluster %d, want %d",
					label, entry.Name(), got, want)
			}
			seen++
		}
		if seen == 0 {
			t.Errorf("%s: no entries with records to check", label)
		}
	}

	assertParent("root", sfRootCluster, root)

	docs := dirOf(t, fs, root, "docs")
	assertParent("docs", sfDocsCluster, docs)

	nested := dirOf(t, fs, docs, "nested")
	assertParent("nested", sfNestedCluster, nested)
}

// TestIdentityFileIDsAreDistinct checks that the composite identity actually
// separates the entries of a volume. A FileID built from a field the parser
// forgot to set would collide everything onto 0:0, and every caller comparing
// FileIDs would then decide every file was the same file.
func TestIdentityFileIDsAreDistinct(t *testing.T) {
	fs := openSuperfloppy(t, true)

	byID := make(map[libxfat.FileID]string)
	for _, entry := range collectAll(t, fs) {
		id, ok := fs.FileID(entry)
		if !ok {
			continue
		}
		if previous, clash := byID[id]; clash {
			t.Errorf("FileID %s is shared by %q and %q", id, previous, entry.Name())
			continue
		}
		byID[id] = entry.Name()
	}

	if len(byID) < 2 {
		t.Fatalf("only %d identifiable entries; the fixture has more", len(byID))
	}
	t.Logf("%d distinct file identities", len(byID))
}

// TestIdentityRegionEntriesHaveNoRecord pins the refusals. $MBR, $FAT1 and $FAT2
// are byte ranges the library synthesises, not directory records, so they have
// neither an address to report nor an identity to compare - and reporting 0:0 for
// all three would collide them into one another and into every other unidentified
// entry.
func TestIdentityRegionEntriesHaveNoRecord(t *testing.T) {
	fs := openSuperfloppy(t, true)

	regions := 0
	for _, entry := range collectAll(t, fs) {
		if _, isRegion := entry.RegionOffset(); !isRegion {
			continue
		}
		regions++

		if offset, ok := entry.EntrySetOffset(); ok {
			t.Errorf("%q is a region entry but reports entry-set offset %d",
				entry.Name(), offset)
		}
		if id, ok := fs.FileID(entry); ok {
			t.Errorf("%q is a region entry but reports FileID %s", entry.Name(), id)
		}
	}

	if regions == 0 {
		t.Fatal("no region entries found; the fixture should synthesise three")
	}
}

// TestIdentityOffsetsAndSlotsAscend checks that both addresses advance in the
// order the entries are yielded within one directory. A slot index that failed to
// carry across a chunk boundary, or an offset taken from the wrong cluster, shows
// up here as a value that goes backwards.
func TestIdentityOffsetsAndSlotsAscend(t *testing.T) {
	fs := openSuperfloppy(t, true)

	root, err := fs.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir: %v", err)
	}

	lastSlot := int64(-1)
	lastOffset := int64(-1)
	for _, entry := range root {
		offset, ok := entry.EntrySetOffset()
		if !ok {
			continue
		}
		slot := int64(entry.EntrySlotIndex())
		if slot <= lastSlot {
			t.Errorf("%q: slot %d does not advance past %d", entry.Name(), slot, lastSlot)
		}
		if offset <= lastOffset {
			t.Errorf("%q: offset %d does not advance past %d", entry.Name(), offset, lastOffset)
		}
		lastSlot, lastOffset = slot, offset
	}
}

// TestIdentityRecoveredEntriesAreLocatedNotIdentified pins the deliberate
// asymmetry for carved entries. The cluster they were found in is known, so their
// records can be pointed at; the directory that listed them is gone, so there is
// no parent and no slot index worth believing, and FileID says so rather than
// inventing one.
func TestIdentityRecoveredEntriesAreLocatedNotIdentified(t *testing.T) {
	image := buildSuperfloppyImage()
	fs := openSuperfloppy(t, true)

	recovered, err := fs.RecoverDeletedEntries()
	if err != nil {
		t.Fatalf("RecoverDeletedEntries: %v", err)
	}
	if len(recovered) == 0 {
		t.Skip("fixture yielded no carved entries")
	}

	for _, entry := range recovered {
		offset, ok := entry.EntrySetOffset()
		if !ok {
			t.Errorf("carved entry %q reports no offset, but the cluster it came from is known",
				entry.Name())
			continue
		}
		if offset+32 > int64(len(image)) {
			t.Fatalf("carved entry %q: offset %d is outside the image", entry.Name(), offset)
		}
		if got := image[offset]; got != entry.EntryType() {
			t.Errorf("carved entry %q: record at %d has type 0x%02x, entry says 0x%02x",
				entry.Name(), offset, got, entry.EntryType())
		}
		if id, ok := fs.FileID(entry); ok {
			t.Errorf("carved entry %q reports FileID %s, but its parent directory is gone",
				entry.Name(), id)
		}
	}
}
