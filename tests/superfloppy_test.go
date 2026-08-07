package test

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
	"unicode/utf16"

	"github.com/aoiflux/libxfat"
)

// This fixture is a superfloppy: an exFAT volume that begins at byte 0 of the
// image with no partition table in front of it, whose volume boot record
// nonetheless records a non-zero PartitionOffset. Imaging tools and superfloppy
// formatters produce this routinely, and it is the shape of image on which
// strict mode used to return an entry for every file with an empty name.

const (
	sfSectorSize       = 512
	sfSectorsPerClust  = 8
	sfClusterSize      = sfSectorSize * sfSectorsPerClust
	sfClusterCount     = 512
	sfFatOffsetSector  = 128
	sfFatSizeSectors   = 8
	sfHeapOffsetSector = sfFatOffsetSector + sfFatSizeSectors
	sfHeapSectors      = sfClusterCount * sfSectorsPerClust
	sfVolumeSectors    = sfHeapOffsetSector + sfHeapSectors

	// The lie: the volume claims to live at LBA 2048 while sitting at byte 0.
	sfClaimedPartitionLBA = 2048
)

// Cluster assignments, written out rather than computed so the expected layout
// is readable next to the assertions.
const (
	sfRootCluster      = 2
	sfBitmapCluster    = 3
	sfUpcaseCluster    = 4
	sfReadmeCluster    = 5
	sfLongNameCluster  = 6
	sfDocsCluster      = 7
	sfNestedCluster    = 8
	sfDeepCluster      = 9 // spans clusters 9 and 10, contiguously
	sfNotesCluster     = 11
	sfFragmentCluster  = 12 // chains 12 -> 14, leaving 13 as a gap
	sfFragmentCluster2 = 14
	sfUnicodeCluster   = 15
	sfErasedCluster    = 16
	sfNamelessCluster  = 17 // a directory whose name records are all NUL
	sfBuriedCluster    = 18 // its child, reachable only by traversing it
	sfLastUsedCluster  = sfBuriedCluster
)

// sfNamelessName is a name made only of NUL code units: the records are present
// and the length is honest, but nothing decodes. Left unnamed, the directory
// would be treated as unreadable and everything under it would vanish.
const sfNamelessName = "\x00\x00"

// sfUnnamedDirName is the placeholder the library is expected to substitute:
// the $Unnamed prefix keyed to the entry's first cluster.
const sfUnnamedDirName = "$Unnamed-17"

const (
	sfLongName    = "a-deliberately-long-file-name-that-spans-several-name-records.txt"
	sfUnicodeName = "unicode-éèü-\U0001F600.bin"
)

// sfEntrySet describes one directory entry set to lay down.
type sfEntrySet struct {
	name       string
	attrs      uint16
	cluster    uint32
	size       uint64
	noFatChain bool
	deleted    bool
}

// sfSpecChecksum is the EntrySetChecksum routine from section 6.3.2 of the
// exFAT specification, transliterated. The fixture computes its checksums the
// way a real formatter would, independently of the library under test.
func sfSpecChecksum(entries []byte) uint16 {
	var checksum uint16
	for index := 0; index < len(entries); index++ {
		if index == 2 || index == 3 {
			continue
		}
		var carry uint16
		if checksum&1 != 0 {
			carry = 0x8000
		}
		checksum = carry + (checksum >> 1) + uint16(entries[index])
	}
	return checksum
}

func sfBuildEntrySet(s sfEntrySet) []byte {
	units := utf16.Encode([]rune(s.name))
	nameRecords := (len(units) + 14) / 15
	secondaries := 1 + nameRecords

	set := make([]byte, (1+secondaries)*32)

	primary := set[0:32]
	primary[0] = 0x85
	if s.deleted {
		primary[0] = 0x05
	}
	primary[1] = byte(secondaries)
	binary.LittleEndian.PutUint16(primary[4:6], s.attrs)
	binary.LittleEndian.PutUint32(primary[8:12], 0x55000000)
	binary.LittleEndian.PutUint32(primary[12:16], 0x55000000)
	binary.LittleEndian.PutUint32(primary[16:20], 0x55000000)

	stream := set[32:64]
	stream[0] = 0xC0
	if s.deleted {
		stream[0] = 0x40
	}
	if s.noFatChain {
		stream[1] = 0x02
	}
	stream[3] = byte(len(units))
	binary.LittleEndian.PutUint64(stream[8:16], s.size)
	binary.LittleEndian.PutUint32(stream[20:24], s.cluster)
	binary.LittleEndian.PutUint64(stream[24:32], s.size)

	for i := 0; i < nameRecords; i++ {
		rec := set[(2+i)*32 : (3+i)*32]
		rec[0] = 0xC1
		if s.deleted {
			rec[0] = 0x41
		}
		chunk := units[i*15:]
		if len(chunk) > 15 {
			chunk = chunk[:15]
		}
		for j, u := range chunk {
			binary.LittleEndian.PutUint16(rec[2+j*2:4+j*2], u)
		}
	}

	binary.LittleEndian.PutUint16(primary[2:4], sfSpecChecksum(set))
	return set
}

// buildSuperfloppyImage assembles a complete, self-consistent exFAT volume.
func buildSuperfloppyImage() []byte {
	image := make([]byte, sfVolumeSectors*sfSectorSize)

	// --- Volume boot record ---
	vbr := image[0 : 12*sfSectorSize]
	copy(vbr[3:11], []byte("EXFAT   "))
	binary.LittleEndian.PutUint64(vbr[0x40:0x48], sfClaimedPartitionLBA)
	binary.LittleEndian.PutUint64(vbr[0x48:0x50], sfVolumeSectors)
	binary.LittleEndian.PutUint32(vbr[0x50:0x54], sfFatOffsetSector)
	binary.LittleEndian.PutUint32(vbr[0x54:0x58], sfFatSizeSectors)
	binary.LittleEndian.PutUint32(vbr[0x58:0x5c], sfHeapOffsetSector)
	binary.LittleEndian.PutUint32(vbr[0x5c:0x60], sfClusterCount)
	binary.LittleEndian.PutUint32(vbr[0x60:0x64], sfRootCluster)
	binary.LittleEndian.PutUint16(vbr[0x68:0x6a], 0x0100)
	vbr[0x6c] = 9 // bytes per sector shift: 512
	vbr[0x6d] = 3 // sectors per cluster shift: 8
	vbr[0x6e] = 1 // number of FATs
	vbr[0x70] = 40
	binary.BigEndian.PutUint16(vbr[0x1fe:0x200], 0x55aa)

	// --- FAT ---
	fat := image[sfFatOffsetSector*sfSectorSize : (sfFatOffsetSector+sfFatSizeSectors)*sfSectorSize]
	setFat := func(cluster, next uint32) {
		binary.LittleEndian.PutUint32(fat[cluster*4:cluster*4+4], next)
	}
	binary.LittleEndian.PutUint32(fat[0:4], 0xfffffff8)
	binary.LittleEndian.PutUint32(fat[4:8], 0xffffffff)
	for cluster := uint32(sfRootCluster); cluster <= sfLastUsedCluster; cluster++ {
		setFat(cluster, 0xffffffff)
	}
	// The one genuinely fragmented file.
	setFat(sfFragmentCluster, sfFragmentCluster2)
	setFat(sfFragmentCluster2, 0xffffffff)

	clusterAt := func(cluster uint32) []byte {
		start := sfHeapOffsetSector*sfSectorSize + int(cluster-2)*sfClusterSize
		return image[start : start+sfClusterSize]
	}

	// --- Allocation bitmap: clusters 2..16 are in use ---
	bitmap := clusterAt(sfBitmapCluster)
	for cluster := uint32(sfRootCluster); cluster <= sfLastUsedCluster; cluster++ {
		index := cluster - 2
		bitmap[index/8] |= 1 << (index % 8)
	}

	// --- Root directory ---
	root := clusterAt(sfRootCluster)
	offset := 0
	appendRecords := func(dst []byte, at int, records []byte) int {
		copy(dst[at:], records)
		return at + len(records)
	}

	bitmapEntry := make([]byte, 32)
	bitmapEntry[0] = 0x81
	binary.LittleEndian.PutUint32(bitmapEntry[20:24], sfBitmapCluster)
	binary.LittleEndian.PutUint64(bitmapEntry[24:32], (sfClusterCount+7)/8)
	offset = appendRecords(root, offset, bitmapEntry)

	upcaseEntry := make([]byte, 32)
	upcaseEntry[0] = 0x82
	binary.LittleEndian.PutUint32(upcaseEntry[20:24], sfUpcaseCluster)
	binary.LittleEndian.PutUint64(upcaseEntry[24:32], 5836)
	offset = appendRecords(root, offset, upcaseEntry)

	label := make([]byte, 32)
	label[0] = 0x83
	labelUnits := utf16.Encode([]rune("EVIDENCE"))
	label[1] = byte(len(labelUnits))
	for i, u := range labelUnits {
		binary.LittleEndian.PutUint16(label[2+i*2:4+i*2], u)
	}
	offset = appendRecords(root, offset, label)

	offset = appendRecords(root, offset, sfBuildEntrySet(sfEntrySet{
		name: "readme.txt", attrs: 0x20, cluster: sfReadmeCluster, size: 100, noFatChain: true,
	}))
	offset = appendRecords(root, offset, sfBuildEntrySet(sfEntrySet{
		name: sfLongName, attrs: 0x20, cluster: sfLongNameCluster, size: 200, noFatChain: true,
	}))
	offset = appendRecords(root, offset, sfBuildEntrySet(sfEntrySet{
		name: sfUnicodeName, attrs: 0x20, cluster: sfUnicodeCluster, size: 12, noFatChain: true,
	}))
	offset = appendRecords(root, offset, sfBuildEntrySet(sfEntrySet{
		name: "docs", attrs: 0x10, cluster: sfDocsCluster, size: sfClusterSize,
	}))
	appendRecords(root, offset, sfBuildEntrySet(sfEntrySet{
		name: "fragmented.bin", attrs: 0x20, cluster: sfFragmentCluster, size: 8000,
	}))

	// --- /docs ---
	docs := clusterAt(sfDocsCluster)
	offset = appendRecords(docs, 0, sfBuildEntrySet(sfEntrySet{
		name: "nested", attrs: 0x10, cluster: sfNestedCluster, size: sfClusterSize,
	}))
	offset = appendRecords(docs, offset, sfBuildEntrySet(sfEntrySet{
		name: "notes.txt", attrs: 0x20, cluster: sfNotesCluster, size: 50, noFatChain: true,
	}))
	offset = appendRecords(docs, offset, sfBuildEntrySet(sfEntrySet{
		name: "erased.txt", attrs: 0x20, cluster: sfErasedCluster, size: 64,
		noFatChain: true, deleted: true,
	}))
	appendRecords(docs, offset, sfBuildEntrySet(sfEntrySet{
		name: sfNamelessName, attrs: 0x10, cluster: sfNamelessCluster, size: sfClusterSize,
	}))

	// --- /docs/nested ---
	nested := clusterAt(sfNestedCluster)
	appendRecords(nested, 0, sfBuildEntrySet(sfEntrySet{
		name: "deep.bin", attrs: 0x20, cluster: sfDeepCluster, size: 5000, noFatChain: true,
	}))

	// --- /docs/<nameless> ---
	nameless := clusterAt(sfNamelessCluster)
	appendRecords(nameless, 0, sfBuildEntrySet(sfEntrySet{
		name: "buried.txt", attrs: 0x20, cluster: sfBuriedCluster, size: 33, noFatChain: true,
	}))

	// --- File content, so extraction has something recognisable to read ---
	for cluster, fill := range map[uint32]byte{
		sfReadmeCluster:   'R',
		sfLongNameCluster: 'L',
		sfUnicodeCluster:  'U',
		sfNotesCluster:    'N',
		sfDeepCluster:     'D',
		sfFragmentCluster: 'F',
		sfBuriedCluster:   'B',
	} {
		data := clusterAt(cluster)
		for i := range data {
			data[i] = fill
		}
	}

	return image
}

func writeSuperfloppy(t *testing.T) (*os.File, int64) {
	t.Helper()

	image := buildSuperfloppyImage()
	path := filepath.Join(t.TempDir(), "superfloppy.exfat")
	if err := os.WriteFile(path, image, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })

	return file, int64(len(image))
}

func openSuperfloppy(t *testing.T, strict bool) *libxfat.ExFAT {
	t.Helper()

	file, size := writeSuperfloppy(t)
	fs, err := libxfat.Open(libxfat.Source{
		Reader:                file,
		Size:                  size,
		Strict:                strict,
		IgnorePartitionOffset: true,
	})
	if err != nil {
		t.Fatalf("open (strict=%v): %v", strict, err)
	}
	return fs
}

func sortedNames(entries []libxfat.Entry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.GetName())
	}
	sort.Strings(names)
	return names
}

func collectAll(t *testing.T, fs *libxfat.ExFAT) []libxfat.Entry {
	t.Helper()

	root, err := fs.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir: %v", err)
	}
	entries, err := fs.GetAllEntries(root)
	if err != nil {
		t.Fatalf("GetAllEntries: %v", err)
	}
	return entries
}

func collectFullPaths(t *testing.T, fs *libxfat.ExFAT) []string {
	t.Helper()

	root, err := fs.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir: %v", err)
	}
	entries, err := fs.GetFullPathIndexableEntries(root, "/")
	if err != nil {
		t.Fatalf("GetFullPathIndexableEntries: %v", err)
	}
	return sortedNames(entries)
}

// TestSuperfloppyStrictYieldsNames is the acceptance test for the bug: on a
// volume whose VBR records a non-zero PartitionOffset, strict mode returned an
// entry set for every file with GetName() == "", which in turn stopped
// directory recursion because a nameless directory is treated as unreadable.
func TestSuperfloppyStrictYieldsNames(t *testing.T) {
	fs := openSuperfloppy(t, true)
	entries := collectAll(t, fs)

	want := []string{
		"$BitMap", "$FAT1", "$MBR", "$OrphanFiles", "$UpCase",
		"buried.txt", "deep.bin", "docs", "erased.txt (deleted)",
		"fragmented.bin", "nested", "notes.txt", "readme.txt",
		sfUnnamedDirName, sfLongName, sfUnicodeName,
	}
	sort.Strings(want)

	got := sortedNames(entries)
	if len(got) != len(want) {
		t.Fatalf("GetAllEntries returned %d entries %q, want %d %q", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}

	for _, entry := range entries {
		if entry.GetName() == "" {
			t.Error("strict mode produced an entry with an empty name")
		}
	}
}

// TestSuperfloppyStrictMatchesOptimistic is the invariant the library owes a
// forensic caller: enabling verification must not change what the volume is
// found to contain.
func TestSuperfloppyStrictMatchesOptimistic(t *testing.T) {
	strictEntries := collectAll(t, openSuperfloppy(t, true))
	optimisticEntries := collectAll(t, openSuperfloppy(t, false))

	strict := sortedNames(strictEntries)
	optimistic := sortedNames(optimisticEntries)

	if len(strict) != len(optimistic) {
		t.Fatalf("strict found %d entries %q, optimistic %d %q",
			len(strict), strict, len(optimistic), optimistic)
	}
	for i := range strict {
		if strict[i] != optimistic[i] {
			t.Errorf("entry %d: strict %q, optimistic %q", i, strict[i], optimistic[i])
		}
	}

	// Sizes and clusters must agree too, not just names.
	byName := make(map[string]libxfat.Entry, len(optimisticEntries))
	for _, entry := range optimisticEntries {
		byName[entry.GetName()] = entry
	}
	for _, entry := range strictEntries {
		other, ok := byName[entry.GetName()]
		if !ok {
			t.Errorf("%q missing from optimistic results", entry.GetName())
			continue
		}
		if entry.GetSize() != other.GetSize() {
			t.Errorf("%q size: strict %d, optimistic %d", entry.GetName(), entry.GetSize(), other.GetSize())
		}
		if entry.GetEntryCluster() != other.GetEntryCluster() {
			t.Errorf("%q cluster: strict %d, optimistic %d",
				entry.GetName(), entry.GetEntryCluster(), other.GetEntryCluster())
		}
	}
}

// TestSuperfloppyFullPathsAreComposed guards the other half of the symptom:
// entry counts were right while every name was empty, so composed paths came
// out as runs of slashes.
func TestSuperfloppyFullPathsAreComposed(t *testing.T) {
	strict := collectFullPaths(t, openSuperfloppy(t, true))
	optimistic := collectFullPaths(t, openSuperfloppy(t, false))

	want := []string{
		"/docs/" + sfUnnamedDirName + "/buried.txt",
		"/docs/nested/deep.bin",
		"/docs/notes.txt",
		"/readme.txt",
		"/" + sfLongName,
		"/" + sfUnicodeName,
	}
	sort.Strings(want)

	if len(strict) != len(want) {
		t.Fatalf("strict full paths = %q, want %q", strict, want)
	}
	for i := range want {
		if strict[i] != want[i] {
			t.Errorf("full path %d = %q, want %q", i, strict[i], want[i])
		}
	}

	if len(optimistic) != len(strict) {
		t.Fatalf("optimistic full paths %q differ from strict %q", optimistic, strict)
	}
	for i := range strict {
		if strict[i] != optimistic[i] {
			t.Errorf("full path %d: strict %q, optimistic %q", i, strict[i], optimistic[i])
		}
	}

	slashesOnly := regexp.MustCompile(`^/+$`)
	for _, path := range strict {
		if slashesOnly.MatchString(path) {
			t.Errorf("full path %q is nothing but separators", path)
		}
	}
}

// TestSuperfloppyStrictVerifiesChecksums confirms the checks strict mode is
// there for actually ran and passed, rather than being skipped into silence.
func TestSuperfloppyStrictVerifiesChecksums(t *testing.T) {
	for _, entry := range collectAll(t, openSuperfloppy(t, true)) {
		if entry.GetEntryType() != 0x85 {
			continue
		}
		if !entry.NameChecksumVerified() {
			t.Errorf("%q did not verify: %v", entry.GetName(), entry.NameChecksumError())
		}
	}
}

// TestSuperfloppyNamelessDirectoryKeepsItsSubtree covers the failure mode that
// made the original bug so quiet: traversal treats a directory with no name as
// unreadable, so anything below it disappears without an error. A directory is
// found by its cluster, not its name, so the library substitutes a placeholder
// and keeps going - and says so, because the placeholder is not evidence.
func TestSuperfloppyNamelessDirectoryKeepsItsSubtree(t *testing.T) {
	entries := collectAll(t, openSuperfloppy(t, true))

	var nameless, buried, found bool
	for _, entry := range entries {
		switch entry.GetName() {
		case sfUnnamedDirName:
			nameless = true
			if !entry.HasSyntheticName() {
				t.Error("the placeholder name is not reported as synthetic")
			}
			if !entry.IsDir() {
				t.Error("the unnamed entry lost its directory attribute")
			}
			if entry.GetEntryCluster() != sfNamelessCluster {
				t.Errorf("unnamed directory cluster = %d, want %d",
					entry.GetEntryCluster(), sfNamelessCluster)
			}
		case "buried.txt":
			buried = true
			if entry.HasSyntheticName() {
				t.Error("buried.txt has a real name but is reported as synthetic")
			}
			if entry.GetSize() != 33 {
				t.Errorf("buried.txt size = %d, want 33", entry.GetSize())
			}
		}
	}
	found = nameless && buried

	if !nameless {
		t.Errorf("the nameless directory is missing; got %q", sortedNames(entries))
	}
	if !buried {
		t.Error("buried.txt was lost: the nameless directory truncated its subtree")
	}
	if !found {
		return
	}

	// Every other entry on the volume has a real name and must not be flagged.
	for _, entry := range entries {
		if entry.GetName() != sfUnnamedDirName && entry.HasSyntheticName() {
			t.Errorf("%q is wrongly reported as having a synthetic name", entry.GetName())
		}
	}
}

// TestSuperfloppyPartitionOffsetCrossCheck keeps the existing contract: the
// claimed offset still has to be reconciled, or explicitly waived.
func TestSuperfloppyPartitionOffsetCrossCheck(t *testing.T) {
	file, size := writeSuperfloppy(t)

	_, err := libxfat.Open(libxfat.Source{Reader: file, Size: size, Strict: true})
	if !errors.Is(err, libxfat.ErrPartitionOffsetMismatch) {
		t.Fatalf("strict open = %v, want ErrPartitionOffsetMismatch", err)
	}

	if _, err := libxfat.Open(libxfat.Source{
		Reader: file, Size: size, Strict: true, PartitionLBA: sfClaimedPartitionLBA,
	}); err != nil {
		t.Fatalf("strict open with PartitionLBA: %v", err)
	}
}
