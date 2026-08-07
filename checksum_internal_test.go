package libxfat

import "testing"

// specEntrySetChecksum is a literal transliteration of the EntrySetChecksum
// routine in section 6.3.2 of the exFAT specification, computed over a whole
// entry set in one pass.
//
// It is deliberately written from the specification rather than in terms of
// exfatDirSetChecksumAdd. A checksum implementation checked only against itself
// proves nothing, which is how the library shipped a version that folded bytes
// 2 and 3 in as zeroes - rotating the accumulator twice more than the spec
// does - and failed verification on every entry set of every real volume.
func specEntrySetChecksum(entries []byte) uint16 {
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

// libEntrySetChecksum accumulates the same set the way the parser does: one
// record at a time, with only the first flagged as the primary.
func libEntrySetChecksum(entries []byte) uint16 {
	var accum uint16
	for offset := 0; offset < len(entries); offset += EXFAT_DIRRECORD_SIZE {
		end := offset + EXFAT_DIRRECORD_SIZE
		if end > len(entries) {
			end = len(entries)
		}
		accum = exfatDirSetChecksumAdd(accum, entries[offset:end], offset == 0)
	}
	return accum
}

func TestExfatDirSetChecksumGoldenVector(t *testing.T) {
	// A fixed set of three records with wholly deterministic contents. The
	// expected value was computed from the specification pseudocode, not from
	// this package.
	const golden = 0x0706

	set := make([]byte, 3*EXFAT_DIRRECORD_SIZE)
	for i := 0; i < EXFAT_DIRRECORD_SIZE; i++ {
		set[i] = byte(i)
		set[EXFAT_DIRRECORD_SIZE+i] = byte(i + 32)
		set[2*EXFAT_DIRRECORD_SIZE+i] = byte(i + 64)
	}
	set[0] = EXFAT_DIRRECORD_FILEDIR
	set[1] = 2
	set[EXFAT_DIRRECORD_SIZE] = EXFAT_DIRRECORD_STREAM_EXT
	set[2*EXFAT_DIRRECORD_SIZE] = EXFAT_DIRRECORD_FILENAME_EXT

	if got := libEntrySetChecksum(set); got != golden {
		t.Fatalf("entry set checksum = 0x%04x, want 0x%04x", got, golden)
	}
}

func TestExfatDirSetChecksumMatchesSpec(t *testing.T) {
	// A cheap deterministic generator; the point is breadth, not randomness.
	state := uint32(0x9e3779b9)
	next := func() byte {
		state = state*1664525 + 1013904223
		return byte(state >> 24)
	}

	for _, secondaries := range []int{1, 2, 3, 17} {
		for trial := 0; trial < 500; trial++ {
			set := make([]byte, (secondaries+1)*EXFAT_DIRRECORD_SIZE)
			for i := range set {
				set[i] = next()
			}
			set[0] = EXFAT_DIRRECORD_FILEDIR
			set[1] = byte(secondaries)

			want := specEntrySetChecksum(set)
			if got := libEntrySetChecksum(set); got != want {
				t.Fatalf("secondaries=%d trial=%d: checksum = 0x%04x, want 0x%04x",
					secondaries, trial, got, want)
			}
		}
	}
}

// TestExfatDirSetChecksumSkipsChecksumField pins the distinction the old
// implementation got wrong: bytes 2 and 3 of the primary record are skipped
// outright, not substituted with zero. Substituting still rotates the
// accumulator, and the two are not the same answer.
func TestExfatDirSetChecksumSkipsChecksumField(t *testing.T) {
	rec := make([]byte, EXFAT_DIRRECORD_SIZE)
	for i := range rec {
		rec[i] = byte(i + 1)
	}
	rec[0] = EXFAT_DIRRECORD_FILEDIR
	rec[2] = 0xAB
	rec[3] = 0xCD

	// Zeroing the checksum field must not change the result: it is not read.
	zeroed := append([]byte(nil), rec...)
	zeroed[2] = 0
	zeroed[3] = 0
	if got, want := exfatDirSetChecksumAdd(0, rec, true), exfatDirSetChecksumAdd(0, zeroed, true); got != want {
		t.Fatalf("checksum depends on the checksum field: 0x%04x vs 0x%04x", got, want)
	}

	// Folding those positions in as zero bytes, which is what the library used
	// to do, gives a different answer. If this ever stops being true the
	// regression it guards has become undetectable.
	var folded uint16
	for i := 0; i < EXFAT_DIRRECORD_SIZE; i++ {
		b := rec[i]
		if i == 2 || i == 3 {
			b = 0
		}
		folded = ((folded >> 1) | (folded << 15)) + uint16(b)
	}
	if got := exfatDirSetChecksumAdd(0, rec, true); got == folded {
		t.Fatalf("skipping and zeroing bytes 2-3 agree (0x%04x); the vector no longer distinguishes them", got)
	}
}

// TestExfatDirSetChecksumSecondaryUsesEveryByte confirms the skip applies only
// to the primary record. In a secondary, offsets 2 and 3 carry name data.
func TestExfatDirSetChecksumSecondaryUsesEveryByte(t *testing.T) {
	rec := make([]byte, EXFAT_DIRRECORD_SIZE)
	rec[0] = EXFAT_DIRRECORD_FILENAME_EXT

	base := exfatDirSetChecksumAdd(0, rec, false)
	rec[2] = 'A'
	if got := exfatDirSetChecksumAdd(0, rec, false); got == base {
		t.Fatal("secondary record checksum ignores offset 2")
	}
}
