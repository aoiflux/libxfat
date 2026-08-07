package libxfat

import (
	"errors"
	"fmt"
	"io"
	"unicode/utf16"
)

// This file implements the volume's up-case table and the name hash that
// depends on it.
//
// Every file entry set records a hash of its own up-cased name, in the stream
// extension entry, independently of the set checksum. It is a second, cheaper
// statement about the same bytes, and the two can disagree: a name rewritten in
// place with the checksum recomputed but the hash left stale reads as intact
// under one check and damaged under the other. For evidence work that
// disagreement is the interesting part, so it is worth being able to ask.
//
// Hashing requires up-casing, and up-casing requires the volume's own table
// rather than a Unicode rule of our choosing - the hash was computed with that
// table, so anything else produces mismatches that mean nothing.

// upcaseCompressionMarker introduces a run of identity mappings: the value
// following it is the number of code units that map to themselves. Real
// formatters lean on this heavily, because most of Unicode up-cases to itself.
const upcaseCompressionMarker = 0xFFFF

// ensureUpcaseTable loads and decompresses the volume's up-case table.
//
// Like ensureBitmapEntry, it will read the root directory if it has not been
// read yet, so call it from a settled state rather than from inside a walk.
func (e *ExFAT) ensureUpcaseTable() error {
	if len(e.vbr.upcaseTable) > 0 {
		return nil
	}

	if e.vbr.upcaseEntry.GetSize() == 0 && e.vbr.dimage != nil && e.vbr.rootDirCluster != 0 {
		if _, err := e.ReadRootDir(); err != nil {
			return err
		}
	}
	if e.vbr.upcaseEntry.GetSize() == 0 {
		return ErrUpcaseTableNotFound
	}

	raw := make([]byte, 0, e.vbr.upcaseEntry.GetSize())
	err := e.vbr.visitEntryData(e.vbr.upcaseEntry, func(_ uint32, chunk []byte) error {
		raw = append(raw, chunk...)
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, ErrEOF) {
		return err
	}
	if len(raw) < 2 {
		return ErrUpcaseTableNotFound
	}

	e.vbr.upcaseTable = decompressUpcaseTable(raw)
	if len(e.vbr.upcaseTable) == 0 {
		return ErrUpcaseTableNotFound
	}
	return nil
}

// decompressUpcaseTable expands the on-disk table into a straight lookup indexed
// by code unit.
//
// The table is a sequence of 16-bit values, each the mapping for the next code
// unit in order, except that FFFFh means "the following value is a count of code
// units that map to themselves". The marker is only a marker in that position:
// a count that happens to be FFFFh is still a count, which is why this walks the
// values with an explicit index rather than ranging over pairs.
func decompressUpcaseTable(raw []byte) []uint16 {
	values := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		values = append(values, uint16(raw[i])|uint16(raw[i+1])<<8)
	}

	table := make([]uint16, 0, len(values))
	for i := 0; i < len(values); i++ {
		if values[i] != upcaseCompressionMarker {
			table = append(table, values[i])
			continue
		}
		// A marker with no count after it is a truncated table; keep what we
		// have rather than inventing the rest.
		if i+1 >= len(values) {
			break
		}
		i++
		for run := 0; run < int(values[i]); run++ {
			table = append(table, uint16(len(table)))
		}
	}
	return table
}

// upcaseUnit folds one code unit through the volume's table. Units at or past
// the end of the table map to themselves, which is what the format intends: the
// table is truncated at the point where every remaining character is its own
// upper case.
func (v *VBR) upcaseUnit(unit uint16) uint16 {
	if int(unit) < len(v.upcaseTable) {
		return v.upcaseTable[unit]
	}
	return unit
}

// nameHash is the NameHash routine from section 7.7.3 of the exFAT
// specification: a 16-bit rotate-and-add over the up-cased name's UTF-16LE
// bytes.
func (v *VBR) nameHash(name string) uint16 {
	var hash uint16
	for _, unit := range utf16.Encode([]rune(name)) {
		unit = v.upcaseUnit(unit)
		hash = ((hash << 15) | (hash >> 1)) + uint16(byte(unit))
		hash = ((hash << 15) | (hash >> 1)) + uint16(byte(unit>>8))
	}
	return hash
}

// UpcaseString folds a string through the volume's own up-case table. It is what
// exFAT means by case-insensitive, which is not the same as Unicode's rules or
// Go's strings.ToUpper: the volume decides, and two volumes may disagree.
//
// It reads the root directory if that has not happened yet.
func (e *ExFAT) UpcaseString(s string) (string, error) {
	if err := e.ensureUpcaseTable(); err != nil {
		return "", err
	}

	units := utf16.Encode([]rune(s))
	for i, unit := range units {
		units[i] = e.vbr.upcaseUnit(unit)
	}
	return string(utf16.Decode(units)), nil
}

// NameHash returns the hash the volume would record for name, computed with the
// volume's own up-case table.
func (e *ExFAT) NameHash(name string) (uint16, error) {
	if err := e.ensureUpcaseTable(); err != nil {
		return 0, err
	}
	return e.vbr.nameHash(name), nil
}

// VerifyNameHash recomputes the entry's name hash and compares it with the value
// recorded in its stream extension entry.
//
// This is a check the entry set checksum does not subsume. The checksum says the
// set's bytes are internally consistent; the hash says the name still matches
// what the volume indexed it under. An entry whose name was rewritten and whose
// checksum was recomputed - but whose hash was not - passes one and fails the
// other, and it is worth being able to tell those cases apart.
//
// It returns nil when the hash matches, an error wrapping ErrNameHashMismatch
// when it does not, and some other error when the check could not be made at
// all - notably ErrUpcaseTableNotFound on a volume with no readable table.
//
// Entries that are not file entry sets - the synthetic $MBR and $FAT1, the
// $BitMap and $UpCase streams - carry no recorded hash and are reported as
// having nothing to check, via ErrNoNameHash.
func (e *ExFAT) VerifyNameHash(entry Entry) error {
	if !entry.IsValid() || entry.IsVirtualEntry() || entry.IsSpecialFile() {
		return ErrNoNameHash
	}
	if err := e.ensureUpcaseTable(); err != nil {
		return err
	}

	computed := e.vbr.nameHash(entry.rawName)
	if computed == entry.nameHash {
		return nil
	}
	return fmt.Errorf("%w: %q records hash 0x%04x, computed 0x%04x",
		ErrNameHashMismatch, entry.rawName, entry.nameHash, computed)
}
