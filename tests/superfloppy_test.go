package test

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/aoiflux/libxfat/v2"
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

	// sfSerialNumber is what the boot sector records as VolumeSerialNumber. Real
	// formatters derive one from the clock; the value itself means nothing, which
	// is the point of asserting that the library reports it unchanged.
	sfSerialNumber = 0xdeadbeef

	// sfVolumeLabel is the label record's contents.
	sfVolumeLabel = "EVIDENCE"
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

	// sfOrphanCluster is deliberately *not* allocated and has no FAT entry. It
	// holds the records of a file whose directory is gone, which is the only
	// thing RecoverDeletedEntries looks at: a deleted record still listed in a
	// live directory, as /docs/erased.txt is, is found by reading that directory
	// instead. The content of an unallocated cluster is undefined by the
	// specification, so leaving records in one costs the fixture no conformance.
	// Cluster 19 is left genuinely empty, so the scan has to cope with both.
	sfOrphanCluster = 20
	sfOrphanTarget  = 21 // where the vanished file's data would have been

	// Allocations that are not part of the contiguous run from the root, so the
	// loops that fill the FAT and the bitmap cannot sweep them and they are listed
	// out explicitly. Each exists to make a specific answer testable.
	//
	// sfUnwrittenCluster backs a file whose ValidDataLength is far short of its
	// DataLength, which is the only way UnwrittenRanges has anything to describe.
	//
	// The three-run chain is three runs because two cannot tell correct coalescing
	// from one-run-per-cluster luck, and the descending chain goes backwards
	// because an implementation comparing "is this cluster different from the last"
	// rather than "is it the next one" merges a backwards pair into a single run
	// covering a negative length.
	sfUnwrittenCluster = 19
	sfThreeRunA        = 22 // chains 22 -> 24 -> 26, gaps at 23 and 25
	sfThreeRunB        = 24
	sfThreeRunC        = 26
	sfDescendHigh      = 30 // chains 30 -> 29
	sfDescendLow       = 29
)

// sfExtraChains are the FAT links for the clusters above, as from -> to pairs with
// 0xffffffff for end of chain.
var sfExtraChains = [][2]uint32{
	{sfUnwrittenCluster, 0xffffffff},
	{sfThreeRunA, sfThreeRunB},
	{sfThreeRunB, sfThreeRunC},
	{sfThreeRunC, 0xffffffff},
	{sfDescendHigh, sfDescendLow},
	{sfDescendLow, 0xffffffff},
}

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
	// validSize is ValidDataLength when set. A pointer rather than a plain
	// uint64 because zero is a meaningful value - an allocation nothing was ever
	// written into - and cannot double as "same as size".
	validSize *uint64
}

// sfValid is validSize's constructor, so a fixture entry can be written inline.
func sfValid(n uint64) *uint64 { return &n }

// sfBootChecksum is the BootChecksum routine from section 3.4 of the exFAT
// specification: a 32-bit rotate-and-add over the first 11 sectors of the boot
// region, skipping the three bytes that are allowed to change without the
// checksum being rewritten - VolumeFlags at 106 and 107, and PercentInUse at
// 112. The result fills the twelfth sector.
func sfBootChecksum(region []byte) uint32 {
	var checksum uint32
	for i := 0; i < len(region); i++ {
		if i == 106 || i == 107 || i == 112 {
			continue
		}
		checksum = ((checksum << 31) | (checksum >> 1)) + uint32(region[i])
	}
	return checksum
}

// sfUpcase folds a code unit through the same table the fixture writes: a-z to
// A-Z, everything else to itself.
func sfUpcase(unit uint16) uint16 {
	if unit >= 'a' && unit <= 'z' {
		return unit - 0x20
	}
	return unit
}

// sfNameHash is the NameHash routine from section 7.7.3 of the exFAT
// specification: a rotate-and-add over the up-cased name's UTF-16LE bytes. It
// lives in the stream extension entry and lets a lookup reject a name without
// reading its name records at all.
func sfNameHash(name string) uint16 {
	var hash uint16
	for _, unit := range utf16.Encode([]rune(name)) {
		unit = sfUpcase(unit)
		for _, b := range []byte{byte(unit), byte(unit >> 8)} {
			hash = ((hash << 15) | (hash >> 1)) + uint16(b)
		}
	}
	return hash
}

// sfTableChecksum is the 32-bit rotate-and-add the up-case table's
// TableChecksum field carries, with no bytes skipped.
func sfTableChecksum(data []byte) uint32 {
	var checksum uint32
	for _, b := range data {
		checksum = ((checksum << 31) | (checksum >> 1)) + uint32(b)
	}
	return checksum
}

// sfSpecChecksum is the EntrySetChecksum routine from section 6.3.3 of the
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
	binary.LittleEndian.PutUint16(stream[4:6], sfNameHash(s.name))
	validSize := s.size
	if s.validSize != nil {
		validSize = *s.validSize
	}
	binary.LittleEndian.PutUint64(stream[8:16], validSize)
	binary.LittleEndian.PutUint32(stream[20:24], s.cluster)
	binary.LittleEndian.PutUint64(stream[24:32], s.size)

	for i := 0; i < nameRecords; i++ {
		rec := set[(2+i)*32 : (3+i)*32]
		rec[0] = 0xC1
		if s.deleted {
			rec[0] = 0x41
		}
		// Slots past the end of the name stay zero, as the specification
		// requires of unused FileName characters.
		for j := 0; j < 15; j++ {
			var unit uint16
			if index := i*15 + j; index < len(units) {
				unit = units[index]
			}
			binary.LittleEndian.PutUint16(rec[2+j*2:4+j*2], unit)
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
	binary.LittleEndian.PutUint32(vbr[0x64:0x68], sfSerialNumber)
	binary.LittleEndian.PutUint16(vbr[0x68:0x6a], 0x0100)
	// VolumeFlags is left zero: clean, first FAT active, no media failure. A test
	// that needs a flag set patches a copy of the image.
	binary.LittleEndian.PutUint16(vbr[0x6a:0x6c], 0)
	vbr[0x6c] = 9 // bytes per sector shift: 512
	vbr[0x6d] = 3 // sectors per cluster shift: 8
	vbr[0x6e] = 1 // number of FATs
	vbr[0x70] = 40
	binary.BigEndian.PutUint16(vbr[0x1fe:0x200], 0x55aa)

	// The eight extended boot sectors each end with ExtendedBootSignature.
	// Without them the boot region is incomplete and a checker rejects the
	// volume before it ever reaches a directory entry.
	for sector := 1; sector <= 8; sector++ {
		end := (sector + 1) * sfSectorSize
		binary.LittleEndian.PutUint32(image[end-4:end], 0xAA550000)
	}

	// Sector 11 carries the boot region's own checksum, repeated to fill it.
	checksum := sfBootChecksum(image[0 : 11*sfSectorSize])
	for offset := 11 * sfSectorSize; offset < 12*sfSectorSize; offset += 4 {
		binary.LittleEndian.PutUint32(image[offset:offset+4], checksum)
	}

	// Sectors 12..23 are the backup boot region, a byte-for-byte copy.
	copy(image[12*sfSectorSize:24*sfSectorSize], image[0:12*sfSectorSize])

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

	// The chains that sit outside the contiguous run from the root.
	for _, link := range sfExtraChains {
		setFat(link[0], link[1])
	}

	clusterAt := func(cluster uint32) []byte {
		start := sfHeapOffsetSector*sfSectorSize + int(cluster-2)*sfClusterSize
		return image[start : start+sfClusterSize]
	}

	// --- Allocation bitmap: clusters 2..16 are in use ---
	bitmap := clusterAt(sfBitmapCluster)
	markAllocated := func(cluster uint32) {
		index := cluster - 2
		bitmap[index/8] |= 1 << (index % 8)
	}
	for cluster := uint32(sfRootCluster); cluster <= sfLastUsedCluster; cluster++ {
		markAllocated(cluster)
	}
	// The clusters the run above does not reach. The gaps between them stay free,
	// which is what makes the chains through them genuinely fragmented.
	for _, link := range sfExtraChains {
		markAllocated(link[0])
	}

	// --- Root directory ---
	root := clusterAt(sfRootCluster)
	offset := 0
	appendRecords := func(dst []byte, at int, records []byte) int {
		copy(dst[at:], records)
		return at + len(records)
	}

	// Real formatters write the volume label first, then the allocation bitmap,
	// then the up-case table, and third-party tools read the root positionally
	// on that assumption. Matching the convention keeps the fixture auditable.
	label := make([]byte, 32)
	label[0] = 0x83
	labelUnits := utf16.Encode([]rune(sfVolumeLabel))
	label[1] = byte(len(labelUnits))
	for i, u := range labelUnits {
		binary.LittleEndian.PutUint16(label[2+i*2:4+i*2], u)
	}
	offset = appendRecords(root, offset, label)

	bitmapEntry := make([]byte, 32)
	bitmapEntry[0] = 0x81
	binary.LittleEndian.PutUint32(bitmapEntry[20:24], sfBitmapCluster)
	binary.LittleEndian.PutUint64(bitmapEntry[24:32], (sfClusterCount+7)/8)
	offset = appendRecords(root, offset, bitmapEntry)

	// A real up-case table, so the entry's TableChecksum can be honest. It
	// covers the Latin-1 range and folds a-z to A-Z; characters past the end of
	// the table map to themselves.
	upcase := clusterAt(sfUpcaseCluster)
	for code := 0; code < 256; code++ {
		mapped := uint16(code)
		if code >= 'a' && code <= 'z' {
			mapped = uint16(code - 0x20)
		}
		binary.LittleEndian.PutUint16(upcase[code*2:code*2+2], mapped)
	}
	upcaseBytes := upcase[:512]

	upcaseEntry := make([]byte, 32)
	upcaseEntry[0] = 0x82
	binary.LittleEndian.PutUint32(upcaseEntry[4:8], sfTableChecksum(upcaseBytes))
	binary.LittleEndian.PutUint32(upcaseEntry[20:24], sfUpcaseCluster)
	binary.LittleEndian.PutUint64(upcaseEntry[24:32], uint64(len(upcaseBytes)))
	offset = appendRecords(root, offset, upcaseEntry)

	offset = appendRecords(root, offset, sfBuildEntrySet(sfEntrySet{
		name: "readme.txt", attrs: 0x20, cluster: sfReadmeCluster, size: 100, noFatChain: true,
	}))
	// Padding is left at zero here, which is what the specification requires of
	// unused FileName characters. This volume is meant to be conformant enough
	// for a third-party checker to bless it; the non-zero-residue case, where
	// only NameLength says where the name ends, is covered by the parser's own
	// tests in TestParseDirTruncatesNameToRecordedLength.
	offset = appendRecords(root, offset, sfBuildEntrySet(sfEntrySet{
		name: sfLongName, attrs: 0x20, cluster: sfLongNameCluster, size: 200,
		noFatChain: true,
	}))
	offset = appendRecords(root, offset, sfBuildEntrySet(sfEntrySet{
		name: sfUnicodeName, attrs: 0x20, cluster: sfUnicodeCluster, size: 12, noFatChain: true,
	}))
	offset = appendRecords(root, offset, sfBuildEntrySet(sfEntrySet{
		name: "docs", attrs: 0x10, cluster: sfDocsCluster, size: sfClusterSize,
	}))
	offset = appendRecords(root, offset, sfBuildEntrySet(sfEntrySet{
		name: "unwritten.bin", attrs: 0x20, cluster: sfUnwrittenCluster, size: 3000,
		validSize: sfValid(100), noFatChain: true,
	}))
	offset = appendRecords(root, offset, sfBuildEntrySet(sfEntrySet{
		name: "threerun.bin", attrs: 0x20, cluster: sfThreeRunA, size: 10000,
	}))
	offset = appendRecords(root, offset, sfBuildEntrySet(sfEntrySet{
		name: "descending.bin", attrs: 0x20, cluster: sfDescendHigh, size: 5000,
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
	offset = appendRecords(docs, offset, sfBuildEntrySet(sfEntrySet{
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

	// --- A deleted directory whose cluster is still free ---
	//
	// It is the only way to reach sfOrphanCluster by name: its record survives in
	// /docs, says the directory began at cluster 20, and records NoFatChain, so
	// the range is known without a chain to follow. Descending it recovers
	// vanished.txt under a real path, where the free-space sweep recovers the same
	// record with no path at all.
	appendRecords(docs, offset, sfBuildEntrySet(sfEntrySet{
		name: "gone", attrs: 0x10, cluster: sfOrphanCluster, size: sfClusterSize,
		noFatChain: true, deleted: true,
	}))

	// --- An unallocated cluster holding the records of a vanished file ---
	orphan := clusterAt(sfOrphanCluster)
	appendRecords(orphan, 0, sfBuildEntrySet(sfEntrySet{
		name: "vanished.txt", attrs: 0x20, cluster: sfOrphanTarget, size: 42,
		noFatChain: true, deleted: true,
	}))

	// --- File content, so extraction has something recognisable to read ---
	//
	// The two multi-cluster files get a different byte in each cluster. A single
	// fill byte cannot tell a reader that fetched the second cluster from one that
	// read the first one twice, which for a fragmented file is exactly the mistake
	// worth catching.
	for cluster, fill := range map[uint32]byte{
		sfReadmeCluster:    'R',
		sfLongNameCluster:  'L',
		sfUnicodeCluster:   'U',
		sfNotesCluster:     'N',
		sfDeepCluster:      'D', // spans 9 and 10, contiguously
		sfDeepCluster + 1:  'd',
		sfFragmentCluster:  'F', // chains 12 -> 14
		sfFragmentCluster2: 'f',
		sfBuriedCluster:    'B',

		// One byte per run, so a reader that visits the runs out of order, or
		// merges two of them, produces content this fixture can distinguish.
		sfUnwrittenCluster: 'W',
		sfThreeRunA:        '1',
		sfThreeRunB:        '2',
		sfThreeRunC:        '3',
		sfDescendHigh:      'H',
		sfDescendLow:       'L',
	} {
		data := clusterAt(cluster)
		for i := range data {
			data[i] = fill
		}
	}

	return image
}

// TestWriteSuperfloppyFixture writes the fixture out for an independent
// implementation to audit. The fixture is hand-built by this package, so the
// parser tests only prove the parser agrees with the builder - if both share a
// misreading of the on-disk format, everything still passes. Handing the image
// to a third-party checker is what breaks that circle:
//
//	LIBXFAT_FIXTURE_OUT=/tmp/fixture.exfat go test ./tests/ -run WriteSuperfloppyFixture
//	fsck.exfat -n /tmp/fixture.exfat
//
// exfatprogs 1.2.2 reports exactly one error against it:
//
//	ERROR: /docs: the name length of a file is wrong
//
// which is the deliberately nameless directory, and confirms that entry is
// genuinely malformed rather than merely unusual. Everything else - the boot
// region and its checksum, the backup boot region, the up-case table and its
// checksum, the allocation bitmap, and every entry set's SetChecksum, NameHash
// and name length - it accepts.
func TestWriteSuperfloppyFixture(t *testing.T) {
	path := os.Getenv("LIBXFAT_FIXTURE_OUT")
	if path == "" {
		t.Skip("set LIBXFAT_FIXTURE_OUT to write the fixture image out")
	}
	if err := os.WriteFile(path, buildSuperfloppyImage(), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Logf("wrote fixture to %s", path)
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
		names = append(names, entry.Name())
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
	entries, err := fs.AllEntries(root)
	if err != nil {
		t.Fatalf("AllEntries: %v", err)
	}
	return entries
}

func collectFullPaths(t *testing.T, fs *libxfat.ExFAT) []string {
	t.Helper()

	root, err := fs.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir: %v", err)
	}
	entries, err := fs.ContiguousFilePaths(root, "/")
	if err != nil {
		t.Fatalf("ContiguousFilePaths: %v", err)
	}
	return sortedNames(entries)
}

// TestSuperfloppyStrictYieldsNames is the acceptance test for the bug: on a
// volume whose VBR records a non-zero PartitionOffset, strict mode returned an
// entry set for every file with Name() == "", which in turn stopped
// directory recursion because a nameless directory is treated as unreadable.
func TestSuperfloppyStrictYieldsNames(t *testing.T) {
	fs := openSuperfloppy(t, true)
	entries := collectAll(t, fs)

	want := []string{
		"$BitMap", "$FAT1", "$MBR", "$OrphanFiles", "$UpCase",
		"buried.txt", "deep.bin", "descending.bin", "docs", "erased.txt (deleted)",
		"fragmented.bin", "gone (deleted)", "nested", "notes.txt", "readme.txt",
		"threerun.bin", "unwritten.bin",
		sfUnnamedDirName, sfLongName, sfUnicodeName,
	}
	// "gone" is a deleted directory. AllEntries reports it but does not
	// descend into it, so vanished.txt is absent here; Walk with
	// DescendDeletedDirectories is what recovers that.
	sort.Strings(want)

	got := sortedNames(entries)
	if len(got) != len(want) {
		t.Fatalf("AllEntries returned %d entries %q, want %d %q", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}

	for _, entry := range entries {
		if entry.Name() == "" {
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
		byName[entry.Name()] = entry
	}
	for _, entry := range strictEntries {
		other, ok := byName[entry.Name()]
		if !ok {
			t.Errorf("%q missing from optimistic results", entry.Name())
			continue
		}
		if entry.Size() != other.Size() {
			t.Errorf("%q size: strict %d, optimistic %d", entry.Name(), entry.Size(), other.Size())
		}
		if entry.FirstCluster() != other.FirstCluster() {
			t.Errorf("%q cluster: strict %d, optimistic %d",
				entry.Name(), entry.FirstCluster(), other.FirstCluster())
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
		// The synthetic entries are indexable and must survive path
		// composition; they identify themselves by name, and rewriting the
		// name before testing that used to drop them.
		"/$MBR",
		"/$FAT1",
		"/docs/" + sfUnnamedDirName + "/buried.txt",
		"/docs/nested/deep.bin",
		"/docs/notes.txt",
		"/readme.txt",
		// Contiguous, so it passes the filter. threerun.bin and descending.bin
		// have FAT chains and are excluded - which is the filter's documented and
		// unhelpful behaviour, pinned here rather than left to be rediscovered.
		"/unwritten.bin",
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
		if entry.EntryType() != 0x85 {
			continue
		}
		if !entry.NameChecksumVerified() {
			t.Errorf("%q did not verify: %v", entry.Name(), entry.NameChecksumError())
		}
	}
}

// TestSuperfloppyFullPathsMatchFlatIndex ties the two indexing calls together.
// They apply the same IsContiguousFile test to the same tree, so they must agree on
// which entries qualify; the only difference should be that one composes paths.
//
// They used to disagree by exactly the two synthetic entries, because composing
// the path first left "$MBR" unrecognisable to IsVirtualEntry.
func TestSuperfloppyFullPathsMatchFlatIndex(t *testing.T) {
	fs := openSuperfloppy(t, true)

	root, err := fs.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir: %v", err)
	}
	flat, err := fs.ContiguousFiles(root)
	if err != nil {
		t.Fatalf("ContiguousFiles: %v", err)
	}
	full, err := fs.ContiguousFilePaths(root, "/")
	if err != nil {
		t.Fatalf("ContiguousFilePaths: %v", err)
	}

	if len(flat) != len(full) {
		t.Fatalf("ContiguousFiles returned %d entries %q, ContiguousFilePaths %d %q",
			len(flat), sortedNames(flat), len(full), sortedNames(full))
	}

	// Every composed path must end in the basename the flat index reported.
	basenames := make(map[string]int, len(flat))
	for _, entry := range flat {
		basenames[entry.Name()]++
	}
	for _, entry := range full {
		path := entry.Name()
		base := path[strings.LastIndex(path, "/")+1:]
		if basenames[base] == 0 {
			t.Errorf("%q has no counterpart in the flat index", path)
			continue
		}
		basenames[base]--
	}
	for name, remaining := range basenames {
		if remaining != 0 {
			t.Errorf("%q appears in the flat index but has no composed path", name)
		}
	}
}

// TestSuperfloppySyntheticEntriesSurvivePathComposition keeps the synthetic
// entries usable after their names become paths: they describe fixed byte
// ranges, and losing the region offset would make them unreadable.
func TestSuperfloppySyntheticEntriesSurvivePathComposition(t *testing.T) {
	fs := openSuperfloppy(t, true)

	root, err := fs.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir: %v", err)
	}
	full, err := fs.ContiguousFilePaths(root, "/")
	if err != nil {
		t.Fatalf("ContiguousFilePaths: %v", err)
	}

	found := map[string]bool{"/$MBR": false, "/$FAT1": false}
	for _, entry := range full {
		name := entry.Name()
		if _, want := found[name]; !want {
			continue
		}
		found[name] = true

		offset, isRegion := entry.RegionOffset()
		if !isRegion {
			t.Errorf("%s no longer reports itself as a region", name)
		}
		if entry.Size() == 0 {
			t.Errorf("%s has zero size", name)
		}
		if offset+entry.Size() > uint64(sfVolumeSectors*sfSectorSize) {
			t.Errorf("%s spans [%d, %d), past the end of the image",
				name, offset, offset+entry.Size())
		}
	}

	for name, ok := range found {
		if !ok {
			t.Errorf("%s is missing from the composed paths", name)
		}
	}
}

// TestSuperfloppyCountClustersAgreesWithClusterList keeps the two descriptions
// of an entry's allocation consistent. They answer the same question, so an
// entry that has no cluster mapping must not get a cluster count either: $MBR
// and $FAT1 are byte ranges outside the heap, and a count derived from their
// size is a number with no referent.
func TestSuperfloppyCountClustersAgreesWithClusterList(t *testing.T) {
	fs := openSuperfloppy(t, true)

	root, err := fs.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir: %v", err)
	}
	all, err := fs.AllEntries(root)
	if err != nil {
		t.Fatalf("AllEntries: %v", err)
	}

	var regions int
	for _, entry := range all {
		if entry.IsDeleted() || entry.Size() == 0 {
			continue
		}

		clusters, _, listErr := fs.ClusterList(entry)
		count, countErr := fs.CountClusters(entry)

		if _, isRegion := entry.RegionOffset(); isRegion {
			regions++
			if !errors.Is(listErr, libxfat.ErrNoClusterMapping) {
				t.Errorf("%s: ClusterList = %v, want ErrNoClusterMapping", entry.Name(), listErr)
			}
			if !errors.Is(countErr, libxfat.ErrNoClusterMapping) {
				t.Errorf("%s: CountClusters = (%d, %v), want ErrNoClusterMapping",
					entry.Name(), count, countErr)
			}
			continue
		}

		if listErr != nil || countErr != nil {
			t.Errorf("%s: ClusterList = %v, CountClusters = %v", entry.Name(), listErr, countErr)
			continue
		}
		if count != len(clusters) {
			t.Errorf("%s: CountClusters = %d, ClusterList returned %d clusters",
				entry.Name(), count, len(clusters))
		}
	}

	if regions == 0 {
		t.Fatal("no region entries in the fixture; the check proved nothing")
	}
}

// TestSuperfloppyNameHashesVerify checks every entry against the hash the
// volume recorded for it, using the volume's own up-case table.
func TestSuperfloppyNameHashesVerify(t *testing.T) {
	fs := openSuperfloppy(t, true)

	root, err := fs.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir: %v", err)
	}
	all, err := fs.AllEntries(root)
	if err != nil {
		t.Fatalf("AllEntries: %v", err)
	}

	var checked int
	for _, entry := range all {
		err := fs.VerifyNameHash(entry)
		if errors.Is(err, libxfat.ErrNoNameHash) {
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", entry.Name(), err)
			continue
		}
		checked++
	}

	if checked == 0 {
		t.Fatal("no entries carried a name hash; the check proved nothing")
	}
	t.Logf("%d name hashes verified", checked)
}

// TestSuperfloppyDetectsCorruptNameHash flips one recorded hash in the image and
// confirms only that entry is reported.
//
// The corruption is applied to the bytes rather than built into the fixture, so
// the fixture stays conformant enough for a third-party checker to bless it -
// see TestWriteSuperfloppyFixture.
func TestSuperfloppyDetectsCorruptNameHash(t *testing.T) {
	image := buildSuperfloppyImage()

	// Root directory layout: label, bitmap, upcase, then readme.txt's entry
	// set. Its stream extension is the fifth record, and NameHash sits at
	// offset 4 within it.
	rootStart := sfHeapOffsetSector * sfSectorSize
	stream := rootStart + 4*32
	if image[stream] != 0xC0 {
		t.Fatalf("expected a stream extension at record 4, found type 0x%02x", image[stream])
	}
	image[stream+4] ^= 0xFF

	path := filepath.Join(t.TempDir(), "corrupt.exfat")
	if err := os.WriteFile(path, image, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open image: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })

	fs, err := libxfat.Open(libxfat.Source{
		Reader: file, Size: int64(len(image)), Strict: true, IgnorePartitionOffset: true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	root, err := fs.ReadRootDir()
	if err != nil {
		t.Fatalf("ReadRootDir: %v", err)
	}
	all, err := fs.AllEntries(root)
	if err != nil {
		t.Fatalf("AllEntries: %v", err)
	}

	var mismatched []string
	for _, entry := range all {
		if err := fs.VerifyNameHash(entry); errors.Is(err, libxfat.ErrNameHashMismatch) {
			mismatched = append(mismatched, entry.Name())
		}
	}

	if len(mismatched) != 1 || mismatched[0] != "readme.txt" {
		t.Fatalf("mismatched entries = %q, want exactly [readme.txt]", mismatched)
	}

	// The entry set checksum still covers the corrupted byte, so the two checks
	// should disagree in the expected direction: this is precisely the case the
	// hash catches and a caller may want to see.
	for _, entry := range all {
		if entry.Name() == "readme.txt" && entry.NameChecksumVerified() {
			t.Error("the set checksum still verifies over a byte that was changed")
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
		switch entry.Name() {
		case sfUnnamedDirName:
			nameless = true
			if !entry.HasSyntheticName() {
				t.Error("the placeholder name is not reported as synthetic")
			}
			if !entry.IsDir() {
				t.Error("the unnamed entry lost its directory attribute")
			}
			if entry.FirstCluster() != sfNamelessCluster {
				t.Errorf("unnamed directory cluster = %d, want %d",
					entry.FirstCluster(), sfNamelessCluster)
			}
		case "buried.txt":
			buried = true
			if entry.HasSyntheticName() {
				t.Error("buried.txt has a real name but is reported as synthetic")
			}
			if entry.Size() != 33 {
				t.Errorf("buried.txt size = %d, want 33", entry.Size())
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
		if entry.Name() != sfUnnamedDirName && entry.HasSyntheticName() {
			t.Errorf("%q is wrongly reported as having a synthetic name", entry.Name())
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
