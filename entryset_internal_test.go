package libxfat

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
)

// testSet describes a file entry set to be laid out on disk by buildEntrySet.
type testSet struct {
	name    string
	attrs   uint16
	cluster uint32
	size    uint64
	deleted bool
	// corruptChecksum flips the checksum recorded in the primary record after
	// the real one has been computed. Because the recorded field is skipped by
	// the checksum routine, this produces a set whose name and payload are
	// entirely intact but whose integrity check fails - which is the case the
	// library used to answer by discarding the name.
	corruptChecksum bool
	// nameRecordType overrides the type byte of the name records, so a test can
	// splice a deleted name record into an allocated set.
	nameRecordType byte
	// namePad fills the name records' unused code units. Formatters zero this
	// padding, but a record reused by a later, shorter name carries whatever the
	// previous one left behind, and only NameLength says where the name stops.
	namePad uint16
}

// buildEntrySet lays out a valid exFAT file entry set: one primary record, one
// stream extension, and as many name records as the name needs, with the entry
// set checksum computed the way the specification defines it.
func buildEntrySet(s testSet) []byte {
	units := utf16.Encode([]rune(s.name))
	nameRecords := (len(units) + 14) / 15
	if nameRecords == 0 {
		nameRecords = 1
	}
	secondaries := 1 + nameRecords

	set := make([]byte, (1+secondaries)*EXFAT_DIRRECORD_SIZE)

	primary := set[0:EXFAT_DIRRECORD_SIZE]
	primary[0] = EXFAT_DIRRECORD_FILEDIR
	if s.deleted {
		primary[0] = EXFAT_DIRRECORD_DEL_FILEDIR
	}
	primary[1] = byte(secondaries)
	putLEShort(primary[4:6], s.attrs)
	putLELong(primary[8:12], 0x50000000)  // created
	putLELong(primary[12:16], 0x50000000) // modified
	putLELong(primary[16:20], 0x50000000) // accessed

	stream := set[EXFAT_DIRRECORD_SIZE : 2*EXFAT_DIRRECORD_SIZE]
	stream[0] = EXFAT_DIRRECORD_STREAM_EXT
	if s.deleted {
		stream[0] = EXFAT_DIRRECORD_DEL_STREAM_EXT
	}
	stream[1] = NOT_FAT_CHAIN_FLAG
	stream[3] = byte(len(units))
	putLELongLong(stream[8:16], s.size) // valid data length
	putLELong(stream[20:24], s.cluster)
	putLELongLong(stream[24:32], s.size)

	nameType := byte(EXFAT_DIRRECORD_FILENAME_EXT)
	if s.deleted {
		nameType = EXFAT_DIRRECORD_DEL_FILENAME_EXT
	}
	if s.nameRecordType != 0 {
		nameType = s.nameRecordType
	}
	for i := 0; i < nameRecords; i++ {
		rec := set[(2+i)*EXFAT_DIRRECORD_SIZE : (3+i)*EXFAT_DIRRECORD_SIZE]
		rec[0] = nameType
		for j := 0; j < 15; j++ {
			unit := s.namePad
			if index := i*15 + j; index < len(units) {
				unit = units[index]
			}
			putLEShort(rec[2+j*2:4+j*2], unit)
		}
	}

	checksum := specEntrySetChecksum(set)
	if s.corruptChecksum {
		checksum ^= 0xFFFF
	}
	putLEShort(primary[2:4], checksum)

	return set
}

func putLEShort(dst []byte, v uint16) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
}

func putLELong(dst []byte, v uint32) {
	for i := 0; i < 4; i++ {
		dst[i] = byte(v >> (8 * i))
	}
}

func putLELongLong(dst []byte, v uint64) {
	for i := 0; i < 8; i++ {
		dst[i] = byte(v >> (8 * i))
	}
}

// newTestParser returns a parser with enough volume geometry for the directory
// entry validators to accept realistic clusters and lengths.
func newTestParser(strict bool) *dirParser {
	vbr := &VBR{
		sectorSize:        512,
		sectorsPerCluster: 8,
		clusterSize:       4096,
		nbClusters:        1024,
	}
	return newDirParser(vbr, !strict, false)
}

// buildDir concatenates entry sets into a directory cluster, terminated by the
// zeroed record that ends directory parsing.
func buildDir(sets ...[]byte) []byte {
	var dir []byte
	for _, set := range sets {
		dir = append(dir, set...)
	}
	return append(dir, make([]byte, EXFAT_DIRRECORD_SIZE)...)
}

func entryNames(entries []Entry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

var testSets = []testSet{
	{name: "go.mod", attrs: ENTRY_ATTR_ATTR_MASK, cluster: 5, size: 46},
	{name: "generator", attrs: ENTRY_ATTR_DIR_MASK, cluster: 6},
	{name: "a-considerably-longer-name-that-needs-three-name-records.txt",
		attrs: ENTRY_ATTR_ATTR_MASK, cluster: 7, size: 4096},
	{name: "unicode-éè-\U0001F600.bin", attrs: ENTRY_ATTR_ATTR_MASK, cluster: 8, size: 12},
	{name: "x", attrs: ENTRY_ATTR_ATTR_MASK, cluster: 9, size: 1},
}

func wantNames() []string {
	names := make([]string, 0, len(testSets))
	for _, s := range testSets {
		names = append(names, s.name)
	}
	return names
}

func buildTestDir() []byte {
	sets := make([][]byte, 0, len(testSets))
	for _, s := range testSets {
		sets = append(sets, buildEntrySet(s))
	}
	return buildDir(sets...)
}

// TestParseDirStrictPreservesNames is the regression this whole change exists
// for: strict mode used to blank the name of every entry set on a valid volume.
func TestParseDirStrictPreservesNames(t *testing.T) {
	entries := newTestParser(true).parseDir(buildTestDir())

	if len(entries) != len(testSets) {
		t.Fatalf("parsed %d entries, want %d", len(entries), len(testSets))
	}

	for i, entry := range entries {
		if entry.Name() != testSets[i].name {
			t.Errorf("entry %d name = %q, want %q", i, entry.Name(), testSets[i].name)
		}
		if !entry.NameChecksumVerified() {
			t.Errorf("entry %d (%q) did not verify", i, entry.Name())
		}
		if entry.NameChecksumMismatch() {
			t.Errorf("entry %d (%q) reports a mismatch", i, entry.Name())
		}
		if err := entry.NameChecksumError(); err != nil {
			t.Errorf("entry %d (%q) NameChecksumError = %v, want nil", i, entry.Name(), err)
		}
		if entry.FirstCluster() != testSets[i].cluster {
			t.Errorf("entry %d cluster = %d, want %d", i, entry.FirstCluster(), testSets[i].cluster)
		}
		if entry.Size() != testSets[i].size {
			t.Errorf("entry %d size = %d, want %d", i, entry.Size(), testSets[i].size)
		}
	}
}

// TestParseDirStrictMatchesOptimistic is the invariant a forensic caller cares
// about: turning verification on must not change what the volume contains.
func TestParseDirStrictMatchesOptimistic(t *testing.T) {
	dir := buildTestDir()

	strict := newTestParser(true).parseDir(dir)
	optimistic := newTestParser(false).parseDir(dir)

	if len(strict) != len(optimistic) {
		t.Fatalf("strict parsed %d entries, optimistic %d", len(strict), len(optimistic))
	}

	strictNames := entryNames(strict)
	optimisticNames := entryNames(optimistic)
	for i := range strictNames {
		if strictNames[i] != optimisticNames[i] {
			t.Errorf("entry %d: strict %q, optimistic %q", i, strictNames[i], optimisticNames[i])
		}
		if strictNames[i] == "" {
			t.Errorf("entry %d has an empty name in strict mode", i)
		}
	}
}

// TestParseDirOptimisticReportsNoVerdict keeps "not checked" distinguishable
// from "checked and passed", so a report cannot claim integrity it never tested.
func TestParseDirOptimisticReportsNoVerdict(t *testing.T) {
	entries := newTestParser(false).parseDir(buildTestDir())

	for _, entry := range entries {
		if entry.NameChecksumVerified() {
			t.Errorf("%q claims verification in optimistic mode", entry.Name())
		}
		if entry.NameChecksumMismatch() {
			t.Errorf("%q claims a mismatch in optimistic mode", entry.Name())
		}
		if _, _, checked := entry.EntrySetChecksums(); checked {
			t.Errorf("%q reports the checksum as checked in optimistic mode", entry.Name())
		}
	}
}

// TestParseDirChecksumMismatchKeepsName is the second half of the fix: a
// damaged set is reported as damaged, not erased.
func TestParseDirChecksumMismatchKeepsName(t *testing.T) {
	dir := buildDir(
		buildEntrySet(testSet{name: "intact.txt", attrs: ENTRY_ATTR_ATTR_MASK, cluster: 5, size: 10}),
		buildEntrySet(testSet{name: "damaged.txt", attrs: ENTRY_ATTR_ATTR_MASK, cluster: 6, size: 20,
			corruptChecksum: true}),
	)

	entries := newTestParser(true).parseDir(dir)
	if len(entries) != 2 {
		t.Fatalf("parsed %d entries, want 2", len(entries))
	}

	if !entries[0].NameChecksumVerified() {
		t.Error("intact.txt did not verify")
	}

	damaged := entries[1]
	if damaged.Name() != "damaged.txt" {
		t.Fatalf("damaged entry name = %q, want %q", damaged.Name(), "damaged.txt")
	}
	if !damaged.NameChecksumMismatch() {
		t.Error("damaged entry does not report a mismatch")
	}
	if damaged.NameChecksumVerified() {
		t.Error("damaged entry claims to have verified")
	}

	err := damaged.NameChecksumError()
	if !errors.Is(err, ErrNameChecksumMismatch) {
		t.Fatalf("NameChecksumError = %v, want one wrapping ErrNameChecksumMismatch", err)
	}
	if !strings.Contains(err.Error(), "damaged.txt") {
		t.Errorf("NameChecksumError = %q, want it to name the entry", err)
	}

	expected, computed, checked := damaged.EntrySetChecksums()
	if !checked {
		t.Error("EntrySetChecksums reports the set as unchecked")
	}
	if expected == computed {
		t.Errorf("EntrySetChecksums returned equal values (0x%04x) for a mismatched set", expected)
	}
}

// TestParseDirRejectChecksumMismatchDropsEntry covers the opt-in strictness, for
// callers who would rather lose the entry than carry an unverified one.
func TestParseDirRejectChecksumMismatchDropsEntry(t *testing.T) {
	dir := buildDir(
		buildEntrySet(testSet{name: "intact.txt", attrs: ENTRY_ATTR_ATTR_MASK, cluster: 5, size: 10}),
		buildEntrySet(testSet{name: "damaged.txt", attrs: ENTRY_ATTR_ATTR_MASK, cluster: 6, size: 20,
			corruptChecksum: true}),
	)

	parser := newTestParser(true)
	parser.rejectChecksumMismatch = true

	entries := parser.parseDir(dir)
	if len(entries) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(entries))
	}
	if entries[0].Name() != "intact.txt" {
		t.Fatalf("surviving entry = %q, want %q", entries[0].Name(), "intact.txt")
	}
}

// TestParseDirEntrySetSpanningChunks exercises the accumulator across a cluster
// boundary, where the checksum, the name units and the secondary countdown all
// have to survive into the next callback.
func TestParseDirEntrySetSpanningChunks(t *testing.T) {
	set := buildEntrySet(testSet{
		name:    "a-name-long-enough-to-span-several-name-records.txt",
		attrs:   ENTRY_ATTR_ATTR_MASK,
		cluster: 11,
		size:    99,
	})

	for split := EXFAT_DIRRECORD_SIZE; split < len(set); split += EXFAT_DIRRECORD_SIZE {
		var entries []Entry
		parser := newTestParser(true)
		parser.resetDirParser()
		parser.parseDirChunk(unlocatedChunk, set[:split], &entries)
		parser.parseDirChunk(unlocatedChunk, buildDir(set[split:]), &entries)

		if len(entries) != 1 {
			t.Fatalf("split at %d: parsed %d entries, want 1", split, len(entries))
		}
		if got := entries[0].Name(); got != "a-name-long-enough-to-span-several-name-records.txt" {
			t.Errorf("split at %d: name = %q", split, got)
		}
		if !entries[0].NameChecksumVerified() {
			t.Errorf("split at %d: entry did not verify", split)
		}
	}
}

// TestParseDirIgnoresStrayNameRecord guards the checksum accumulator against
// name records that belong to no set - slack, or a set whose primary record was
// overwritten. Folding them in corrupts the next real set's checksum and
// prepends their bytes to its name.
func TestParseDirIgnoresStrayNameRecord(t *testing.T) {
	stray := make([]byte, EXFAT_DIRRECORD_SIZE)
	stray[0] = EXFAT_DIRRECORD_FILENAME_EXT
	for i, r := range "STRAY" {
		putLEShort(stray[2+i*2:4+i*2], uint16(r))
	}

	dir := buildDir(
		stray,
		buildEntrySet(testSet{name: "real.txt", attrs: ENTRY_ATTR_ATTR_MASK, cluster: 5, size: 10}),
	)

	entries := newTestParser(true).parseDir(dir)
	if len(entries) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(entries))
	}
	if got := entries[0].Name(); got != "real.txt" {
		t.Fatalf("name = %q, want %q", got, "real.txt")
	}
	if !entries[0].NameChecksumVerified() {
		t.Error("the stray record corrupted the following set's checksum")
	}
}

// TestParseDirRequiresStreamExtension rejects a set with no stream extension.
// It has no cluster, no length and no name length, so emitting it would invent
// a zero-byte file that the volume does not contain.
func TestParseDirRequiresStreamExtension(t *testing.T) {
	set := buildEntrySet(testSet{name: "nofile.txt", attrs: ENTRY_ATTR_ATTR_MASK, cluster: 5, size: 10})
	// Replace the stream extension with a second name record, keeping the
	// secondary count intact.
	copy(set[EXFAT_DIRRECORD_SIZE:2*EXFAT_DIRRECORD_SIZE], set[2*EXFAT_DIRRECORD_SIZE:3*EXFAT_DIRRECORD_SIZE])

	if entries := newTestParser(true).parseDir(buildDir(set)); len(entries) != 0 {
		t.Fatalf("parsed %d entries from a set with no stream extension, want 0", len(entries))
	}
}

// TestParseDirRejectsDeletedNameInAllocatedSet stops a deleted name record from
// being spliced onto a live file, which the type check alone permits because it
// masks off the in-use bit.
func TestParseDirRejectsDeletedNameInAllocatedSet(t *testing.T) {
	set := buildEntrySet(testSet{
		name:           "live.txt",
		attrs:          ENTRY_ATTR_ATTR_MASK,
		cluster:        5,
		size:           10,
		nameRecordType: EXFAT_DIRRECORD_DEL_FILENAME_EXT,
	})

	if entries := newTestParser(true).parseDir(buildDir(set)); len(entries) != 0 {
		t.Fatalf("parsed %d entries, want 0: a deleted name record joined an allocated set", len(entries))
	}
}

// TestParseDirTruncatesNameToRecordedLength covers a name record whose padding
// past the end of the name is not zeroed - the state a record is left in when a
// longer name is overwritten by a shorter one. NameLength is the only thing that
// says where the name stops; without honouring it the residue is read as part of
// the name, and every path built from it is wrong.
func TestParseDirTruncatesNameToRecordedLength(t *testing.T) {
	const name = "twenty-char-name.txt" // exactly 20 units: 15 + 5, leaving 10 padded

	dir := buildDir(buildEntrySet(testSet{
		name:    name,
		attrs:   ENTRY_ATTR_ATTR_MASK,
		cluster: 5,
		size:    10,
		namePad: 'X',
	}))

	entries := newTestParser(true).parseDir(dir)
	if len(entries) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(entries))
	}
	if got := entries[0].Name(); got != name {
		t.Fatalf("name = %q, want %q: padding past NameLength leaked into the name", got, name)
	}
	if !entries[0].NameChecksumVerified() {
		t.Error("the padded set did not verify; the padding must be part of the checksum")
	}
	if got, want := entries[0].NameLength(), byte(len(name)); got != want {
		t.Errorf("NameLength() = %d, want %d", got, want)
	}
}

// TestParseDirTruncationSurvivesRecordBoundaries repeats the check at every name
// length that leaves a partly-filled final record, since the boundary between
// "name" and "residue" moves with it.
func TestParseDirTruncationSurvivesRecordBoundaries(t *testing.T) {
	for length := 1; length <= 45; length++ {
		name := strings.Repeat("n", length)

		dir := buildDir(buildEntrySet(testSet{
			name:    name,
			attrs:   ENTRY_ATTR_ATTR_MASK,
			cluster: 5,
			size:    10,
			namePad: 'Z',
		}))

		entries := newTestParser(true).parseDir(dir)
		if len(entries) != 1 {
			t.Fatalf("length %d: parsed %d entries, want 1", length, len(entries))
		}
		if got := entries[0].Name(); got != name {
			t.Fatalf("length %d: name = %q, want %q", length, got, name)
		}
	}
}

// TestParseDirSubstitutesPlaceholderForNamelessSet covers an entry set whose
// name records decode to nothing. An empty name makes a directory look
// unreadable to NonParsable, so its whole subtree would be dropped silently;
// the entry is located by cluster, so a placeholder keeps it addressable.
func TestParseDirSubstitutesPlaceholderForNamelessSet(t *testing.T) {
	dir := buildDir(
		// Name records present, length honest, but every code unit is NUL.
		buildEntrySet(testSet{name: "\x00\x00", attrs: ENTRY_ATTR_DIR_MASK, cluster: 42, size: 4096}),
		buildEntrySet(testSet{name: "named.txt", attrs: ENTRY_ATTR_ATTR_MASK, cluster: 43, size: 10}),
	)

	entries := newTestParser(true).parseDir(dir)
	if len(entries) != 2 {
		t.Fatalf("parsed %d entries, want 2", len(entries))
	}

	nameless := entries[0]
	if got, want := nameless.Name(), UNNAMED+"-42"; got != want {
		t.Fatalf("nameless entry name = %q, want %q", got, want)
	}
	if !nameless.HasSyntheticName() {
		t.Error("placeholder name is not reported as synthetic")
	}
	if nameless.HasNoName() {
		t.Error("placeholder entry still reports HasNoName, so traversal will skip it")
	}
	if nameless.NonParsable() {
		t.Error("placeholder directory is still NonParsable, so its subtree is lost")
	}
	if !nameless.NameChecksumVerified() {
		t.Error("substituting the name disturbed checksum verification")
	}

	named := entries[1]
	if named.HasSyntheticName() {
		t.Error("an entry with a real name is reported as synthetic")
	}
}

// TestParseDirPlaceholderIsStableAndDistinct keeps the placeholder useful as a
// path component: two nameless siblings must not collide, and the same entry
// must produce the same name on every run.
func TestParseDirPlaceholderIsStableAndDistinct(t *testing.T) {
	dir := buildDir(
		buildEntrySet(testSet{name: "\x00", attrs: ENTRY_ATTR_DIR_MASK, cluster: 7, size: 4096}),
		buildEntrySet(testSet{name: "\x00", attrs: ENTRY_ATTR_DIR_MASK, cluster: 8, size: 4096}),
	)

	first := newTestParser(true).parseDir(dir)
	second := newTestParser(true).parseDir(dir)

	if len(first) != 2 {
		t.Fatalf("parsed %d entries, want 2", len(first))
	}
	if first[0].Name() == first[1].Name() {
		t.Fatalf("both nameless siblings are called %q", first[0].Name())
	}
	for i := range first {
		if first[i].Name() != second[i].Name() {
			t.Errorf("entry %d: %q on the first pass, %q on the second",
				i, first[i].Name(), second[i].Name())
		}
	}
}

// TestParseDirDeletedSetKeepsNameInStrictMode covers the carving path, which
// had the same blank-on-mismatch behaviour as the allocated one.
func TestParseDirDeletedSetKeepsNameInStrictMode(t *testing.T) {
	set := buildEntrySet(testSet{
		name:    "erased.txt",
		attrs:   ENTRY_ATTR_ATTR_MASK,
		cluster: 5,
		size:    10,
		deleted: true,
	})

	entries := newTestParser(true).parseDeletedDirEntries(unlocatedChunk, buildDir(set))
	if len(entries) != 1 {
		t.Fatalf("parsed %d deleted entries, want 1", len(entries))
	}
	if got, want := entries[0].Name(), "erased.txt"; got != want {
		t.Fatalf("name = %q, want %q", got, want)
	}
	if !entries[0].NameChecksumVerified() {
		t.Error("deleted entry did not verify")
	}
}
