package libxfat

import (
	"testing"
	"unicode/utf16"
)

func le16(values ...uint16) []byte {
	raw := make([]byte, 0, len(values)*2)
	for _, v := range values {
		raw = append(raw, byte(v), byte(v>>8))
	}
	return raw
}

func TestDecompressUpcaseTableUncompressed(t *testing.T) {
	// Identity for 0..'`', then a-z folded, which is the shape of a small
	// hand-written table.
	values := make([]uint16, 128)
	for i := range values {
		values[i] = uint16(i)
		if i >= 'a' && i <= 'z' {
			values[i] = uint16(i - 0x20)
		}
	}

	table := decompressUpcaseTable(le16(values...))
	if len(table) != 128 {
		t.Fatalf("table length = %d, want 128", len(table))
	}
	if table['a'] != 'A' || table['z'] != 'Z' {
		t.Errorf("a->%c z->%c, want A and Z", table['a'], table['z'])
	}
	if table['A'] != 'A' || table['0'] != '0' {
		t.Error("already-upper and non-letter units did not map to themselves")
	}
}

// TestDecompressUpcaseTableExpandsRuns covers the format real formatters
// actually write: most of Unicode maps to itself, so identity runs are
// collapsed behind an FFFFh marker.
func TestDecompressUpcaseTableExpandsRuns(t *testing.T) {
	// 0..'`' identity as a run, then a-z folded, then a trailing identity run.
	raw := le16(upcaseCompressionMarker, 'a')
	for c := uint16('a'); c <= 'z'; c++ {
		raw = append(raw, le16(c-0x20)...)
	}
	raw = append(raw, le16(upcaseCompressionMarker, 5)...)

	table := decompressUpcaseTable(raw)

	if want := int('a') + 26 + 5; len(table) != want {
		t.Fatalf("table length = %d, want %d", len(table), want)
	}
	for c := 0; c < 'a'; c++ {
		if table[c] != uint16(c) {
			t.Fatalf("run entry %d maps to %d, want itself", c, table[c])
		}
	}
	if table['a'] != 'A' || table['m'] != 'M' || table['z'] != 'Z' {
		t.Error("folded range is wrong")
	}
	for c := int('z') + 1; c < len(table); c++ {
		if table[c] != uint16(c) {
			t.Errorf("trailing run entry %d maps to %d, want itself", c, table[c])
		}
	}
}

// TestDecompressUpcaseTableCountOfFFFF pins the one genuinely ambiguous case:
// FFFFh is a marker only in the value position. A run whose *length* is FFFFh is
// an ordinary count, not a second marker.
func TestDecompressUpcaseTableCountOfFFFF(t *testing.T) {
	raw := le16(upcaseCompressionMarker, upcaseCompressionMarker, 'x')

	table := decompressUpcaseTable(raw)

	if len(table) != 0xFFFF+1 {
		t.Fatalf("table length = %d, want %d", len(table), 0xFFFF+1)
	}
	for _, probe := range []int{0, 1, 0x1234, 0xFFFE} {
		if table[probe] != uint16(probe) {
			t.Errorf("entry %d maps to %d, want itself", probe, table[probe])
		}
	}
	if table[0xFFFF] != 'x' {
		t.Errorf("value after the run maps to %d, want 'x'", table[0xFFFF])
	}
}

func TestDecompressUpcaseTableTruncated(t *testing.T) {
	// A marker with no count after it, and an odd trailing byte. Neither may
	// panic, and neither may invent mappings.
	for _, raw := range [][]byte{
		le16('A', upcaseCompressionMarker),
		append(le16('A', 'B'), 0x00),
		{},
		{0x41},
	} {
		table := decompressUpcaseTable(raw)
		for i, unit := range table {
			if unit == 0 && i != 0 {
				t.Errorf("truncated input produced a zero mapping at %d", i)
			}
		}
	}
}

func TestUpcaseUnitFallsBackToIdentity(t *testing.T) {
	v := upcaseTable{0, 1, 2}

	if got := v.unit(1); got != 1 {
		t.Errorf("in-table unit = %d, want 1", got)
	}
	// Past the end of the table, and the format says such units are already
	// their own upper case.
	for _, unit := range []uint16{3, 0x1000, 0xFFFF} {
		if got := v.unit(unit); got != unit {
			t.Errorf("unit %d past the table = %d, want itself", unit, got)
		}
	}
}

// TestNameHashMatchesSpec checks the hash against the specification's routine,
// transliterated separately, over names that exercise folding, non-ASCII and
// surrogate pairs.
func TestNameHashMatchesSpec(t *testing.T) {
	table := make([]uint16, 256)
	for i := range table {
		table[i] = uint16(i)
		if i >= 'a' && i <= 'z' {
			table[i] = uint16(i - 0x20)
		}
	}
	v := upcaseTable(table)

	specHash := func(units []uint16) uint16 {
		var hash uint16
		for _, unit := range units {
			for _, b := range []byte{byte(unit), byte(unit >> 8)} {
				var carry uint16
				if hash&1 != 0 {
					carry = 0x8000
				}
				hash = carry + (hash >> 1) + uint16(b)
			}
		}
		return hash
	}

	for _, name := range []string{
		"readme.txt", "README.TXT", "MiXeDcAsE", "", "a",
		"unicode-éèü.bin", "emoji-\U0001F600.bin",
		"a-deliberately-long-file-name-that-spans-several-name-records.txt",
	} {
		upcased := make([]uint16, 0, len(name))
		for _, unit := range utf16.Encode([]rune(name)) {
			upcased = append(upcased, v.unit(unit))
		}
		if got, want := v.nameHash(name), specHash(upcased); got != want {
			t.Errorf("nameHash(%q) = 0x%04x, want 0x%04x", name, got, want)
		}
	}
}

// TestNameHashIsCaseInsensitive is the property the hash exists to provide.
func TestNameHashIsCaseInsensitive(t *testing.T) {
	table := make([]uint16, 256)
	for i := range table {
		table[i] = uint16(i)
		if i >= 'a' && i <= 'z' {
			table[i] = uint16(i - 0x20)
		}
	}
	v := upcaseTable(table)

	if v.nameHash("ReadMe.TXT") != v.nameHash("readme.txt") {
		t.Error("hash distinguishes case, but the table folds it")
	}
	if v.nameHash("readme.txt") == v.nameHash("readme.txu") {
		t.Error("hash fails to distinguish different names")
	}
}
