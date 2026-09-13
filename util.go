package libxfat

import (
	"fmt"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

func IsEntryTypeValidRecord(etype byte) bool {
	return etype == EXFAT_DIRRECORD_FILEDIR || etype == EXFAT_DIRRECORD_BITMAP || etype == EXFAT_DIRRECORD_UPCASE
}

func countBitmap(bitmapContent []byte) uint32 {
	counter := bitmapCounter{}
	counter.write(bitmapContent)
	return counter.count()
}

type bitmapCounter struct {
	tail    [4]byte
	tailLen int
	total   uint32
}

func (c *bitmapCounter) write(chunk []byte) {
	if c.tailLen > 0 {
		need := 4 - c.tailLen
		if len(chunk) < need {
			copy(c.tail[c.tailLen:], chunk)
			c.tailLen += len(chunk)
			return
		}
		copy(c.tail[c.tailLen:], chunk[:need])
		c.total += countBits(unpackLELong(c.tail[:]))
		c.tailLen = 0
		chunk = chunk[need:]
	}

	for len(chunk) >= 4 {
		c.total += countBits(unpackLELong(chunk[:4]))
		chunk = chunk[4:]
	}

	if len(chunk) > 0 {
		copy(c.tail[:], chunk)
		c.tailLen = len(chunk)
	}
}

func (c *bitmapCounter) count() uint32 {
	if c.tailLen == 0 {
		return c.total
	}
	var padded [4]byte
	copy(padded[:], c.tail[:c.tailLen])
	return c.total + countBits(unpackLELong(padded[:]))
}

func countBits(bitn uint32) uint32 {
	bitn = (bitn & 0x55555555) + ((bitn & 0xAAAAAAAA) >> 1)
	bitn = (bitn & 0x33333333) + ((bitn & 0xCCCCCCCC) >> 2)
	bitn = (bitn & 0x0F0F0F0F) + ((bitn & 0xF0F0F0F0) >> 4)
	bitn = (bitn & 0x00FF00FF) + ((bitn & 0xFF00FF00) >> 8)
	bitn = (bitn & 0x0000FFFF) + ((bitn & 0xFFFF0000) >> 16)
	return bitn
}

// UnicodeFromAscii returns Unicode from raw utf16 data.
func unicodeFromAscii(raw []byte, unicodeCharCount int) string {
	// `VolumeLabel` is a Unicode-encoded string and the character-count
	// corresponds to the number of Unicode characters. The character-count may
	// still include trailing NULs, so we intentionally skip over those.

	decodedString := make([]rune, 0, unicodeCharCount)
	for i := 0; i < unicodeCharCount; i++ {
		wchar1 := uint16(raw[i*2+1])
		wchar2 := uint16(raw[i*2])

		bytes := []uint16{wchar1<<8 | wchar2}
		runes := utf16.Decode(bytes)

		if runes[0] == 0 {
			continue
		}

		decodedString = append(decodedString, runes...)
	}

	return string(decodedString)
}

// exfatDirSetChecksumAdd updates the running 16-bit checksum for a 32-byte
// directory record, following EntrySetChecksum in section 6.3.3 of the exFAT
// specification.
//
// For the first FILE directory entry in a set the checksum field (bytes 2 and
// 3) is skipped outright: the spec's loop does `continue`, so those positions
// contribute neither their value nor a rotation. Folding them in as zero bytes
// instead - which is what this function used to do - still rotates the
// accumulator twice, permanently desynchronising it from the on-disk value and
// making verification fail for effectively every entry set.
func exfatDirSetChecksumAdd(accum uint16, record []byte, isFileDir bool) uint16 {
	// exFAT directory record size is fixed (32 bytes), but be defensive.
	limit := EXFAT_DIRRECORD_SIZE
	if len(record) < limit {
		limit = len(record)
	}
	for i := 0; i < limit; i++ {
		if isFileDir && (i == 2 || i == 3) {
			continue
		}
		// Rotate right by 1 and add the byte (keep 16-bit)
		accum = ((accum >> 1) | (accum << 15)) + uint16(record[i])
	}
	return accum
}

// appendUTF16LEUnits decodes raw little-endian bytes into dst as UTF-16 code
// units, reading up to maxUnits of them, or all available pairs when maxUnits
// is not positive.
//
// Appending into a caller-owned buffer matters here: this runs once per name
// record, and returning a fresh slice meant an allocation per record that was
// copied into the name buffer and immediately dropped.
func appendUTF16LEUnits(dst []uint16, raw []byte, maxUnits int) []uint16 {
	nPairs := len(raw) / 2
	if maxUnits > 0 && nPairs > maxUnits {
		nPairs = maxUnits
	}
	for i := 0; i < nPairs; i++ {
		dst = append(dst, uint16(raw[i*2])|uint16(raw[i*2+1])<<8)
	}
	return dst
}

// unnamedEntryName builds the placeholder for an entry that carries no name on
// disk. Keying it to the first cluster keeps it stable between runs and distinct
// between siblings, so a report or an extraction tree can address the entry.
func unnamedEntryName(cluster uint32) string {
	if cluster == 0 {
		return UNNAMED
	}
	return fmt.Sprintf("%s-%d", UNNAMED, cluster)
}

// dropNULs compacts units in place, removing the zero code units that pad a
// name record out to its fixed width.
func dropNULs(units []uint16) []uint16 {
	filtered := units[:0]
	for _, u := range units {
		if u != 0 {
			filtered = append(filtered, u)
		}
	}
	return filtered
}

// appendUTF16AsUTF8 decodes UTF-16 code units into dst as UTF-8, matching
// utf16.Decode's treatment of unpaired surrogates.
//
// It exists to avoid the intermediate []rune that utf16.Decode allocates: every
// name on the volume passed through a rune slice on its way to a string, so a
// name cost two allocations where only one is unavoidable.
func appendUTF16AsUTF8(dst []byte, units []uint16) []byte {
	const (
		surr1 = 0xd800
		surr2 = 0xdc00
		surr3 = 0xe000
	)

	for i := 0; i < len(units); i++ {
		unit := units[i]
		switch {
		case unit < surr1, surr3 <= unit:
			dst = utf8.AppendRune(dst, rune(unit))
		case unit < surr2 && i+1 < len(units) &&
			surr2 <= units[i+1] && units[i+1] < surr3:
			dst = utf8.AppendRune(dst, utf16.DecodeRune(rune(unit), rune(units[i+1])))
			i++
		default:
			dst = utf8.AppendRune(dst, unicode.ReplacementChar)
		}
	}
	return dst
}

func getRange(index uint32, count uint64) []uint32 {
	list := make([]uint32, 0, count)
	for count != 0 {
		list = append(list, index)
		count--
		index++
	}
	return list
}
