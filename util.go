package libxfat

import (
	"fmt"
	"unicode/utf16"
)

func IsEntryTypeValidRecord(etype byte) bool {
	return etype == EXFAT_DIRRECORD_FILEDIR || etype == EXFAT_DIRRECORD_BITMAP || etype == EXFAT_DIRRECORD_UPCASE
}

func countBitmap(bitmapContent []byte) uint32 {
	length := len(bitmapContent) / 4
	rem := len(bitmapContent) % 4
	var allocated uint32

	for i := 0; i < length; i++ {
		allocated += countBits(unpackLELong(bitmapContent[i*4 : (i+1)*4]))
	}

	if rem > 0 {
		val := unpackLELong(bitmapContent[len(bitmapContent)-4:])
		switch val {
		case 3:
			val = val & 0xffffff00
		case 2:
			val = val & 0xffff0000
		default:
			val = val & 0xff000000
		}
		allocated += countBits(val)
	}

	return allocated
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
	// still include trailing NULs, sowe intentional skip over those.

	decodedString := make([]rune, 0)
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

func getDateTimeString(datetime, ms uint32) string {
	year := (datetime >> 25) + 1980
	month := (datetime >> 21) & 0xf
	day := (datetime >> 16) & 0x1f
	hour := (datetime >> 11) & 0x1f
	min := (datetime >> 5) & 0x3f
	sec := (datetime & 0x1f) << 1 //(15 means 30secs)

	datetimestring := fmt.Sprintf("%d/%d/%d %d:%d:%d:%d", year, month, day, hour, min, sec, ms)
	return datetimestring
}

func getFileAttributes(attr uint16) string {
	const char = '-'
	arc := char
	dir := char
	sys := char
	hid := char
	ro := char

	if attr&ENTRY_ATTR_ATTR_MASK != 0 {
		arc = 'a'
	}
	if attr&ENTRY_ATTR_DIR_MASK != 0 {
		dir = 'd'
	}
	if attr&ENTRY_ATTR_SYSTEM_MASK != 0 {
		sys = 's'
	}
	if attr&ENTRY_ATTR_HIDDEN_MASK != 0 {
		hid = 'h'
	}
	if attr&ENTRY_ATTR_RO_MASK != 0 {
		ro = 'r'
	}

	fileAttributeString := fmt.Sprintf("%c%c%c%c%c", arc, dir, sys, hid, ro)
	return fileAttributeString
}

func humanize(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func getRange(index uint32, count uint64) []uint32 {
	var list []uint32
	for count != 0 {
		list = append(list, index)
		count--
		index++
	}
	return list
}
