package libxfat

import "time"

// exFAT records three timestamps per file entry: creation, last modification
// and last access. There is no fourth, Unix-ctime-like "metadata changed" time,
// so in MACB terms an exFAT entry supplies M, A and C, with B (birth) being the
// same value as creation.
//
// Each timestamp is a packed DOS-style wall-clock reading with two-second
// resolution. Creation and modification carry an additional 0..199 count of
// 10-millisecond increments; access does not. None of that is absolute on its
// own: the accompanying UTC offset byte is what anchors the reading to a real
// instant, and it is stored in a separate field of the entry.
const (
	// utcOffsetValidMask marks the offset byte as meaningful.
	utcOffsetValidMask = 0x80
	// utcOffsetValueMask isolates the signed 7-bit offset.
	utcOffsetValueMask = 0x7F
	// utcOffsetUnit is the granularity of the encoded offset.
	utcOffsetUnit = 15 * time.Minute
	// maxTenMsIncrement is the largest legal 10ms increment (1.99 seconds).
	maxTenMsIncrement = 199
)

// Timestamps carries the full temporal picture of an entry, including the
// provenance needed to defend it: whether each reading was anchored by a
// recorded UTC offset, or merely assumed to be UTC because none was stored.
type Timestamps struct {
	// Modified, Created and Accessed are normalised to UTC. A zero value means
	// the entry carries no usable timestamp in that slot; test with IsZero.
	Modified time.Time
	Created  time.Time
	Accessed time.Time

	// ModifiedLocal, CreatedLocal and AccessedLocal are the wall-clock readings
	// exactly as stored on the volume. When the corresponding OffsetValid field
	// is true they carry a fixed zone, so formatting them shows the offset the
	// volume recorded; otherwise their location is UTC by assumption.
	ModifiedLocal time.Time
	CreatedLocal  time.Time
	AccessedLocal time.Time

	// *OffsetValid reports whether the volume actually recorded a UTC offset for
	// that timestamp. When false, the UTC value is the stored wall clock taken
	// at face value and may be wrong by the writing system's zone.
	ModifiedOffsetValid bool
	CreatedOffsetValid  bool
	AccessedOffsetValid bool
}

// decodeTimestamp unpacks one exFAT timestamp.
//
// It returns the zero time for an unset field (packed == 0) and for any reading
// that is not a real calendar date, rather than letting an out-of-range field
// wrap into a plausible-looking but fictitious date.
func decodeTimestamp(packed uint32, tenms byte, utcOffset byte) (utc, local time.Time, offsetValid bool) {
	if packed == 0 {
		return time.Time{}, time.Time{}, false
	}

	year := int(packed>>25) + 1980
	month := int((packed >> 21) & 0x0F)
	day := int((packed >> 16) & 0x1F)
	hour := int((packed >> 11) & 0x1F)
	minute := int((packed >> 5) & 0x3F)
	second := int(packed&0x1F) * 2

	if month < 1 || month > 12 || day < 1 || day > 31 ||
		hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, time.Time{}, false
	}

	loc := time.UTC
	if utcOffset&utcOffsetValidMask != 0 {
		offsetValid = true
		units := int(utcOffset & utcOffsetValueMask)
		// Bits 0..6 are two's complement, so anything with bit 6 set is negative.
		if units >= 0x40 {
			units -= 0x80
		}
		loc = time.FixedZone("", int(time.Duration(units)*utcOffsetUnit/time.Second))
	}

	local = time.Date(year, time.Month(month), day, hour, minute, second, 0, loc)

	// time.Date normalises impossible dates (31 February becomes 3 March), which
	// would turn corrupt metadata into a confident-looking answer. Reject it.
	if local.Year() != year || local.Month() != time.Month(month) || local.Day() != day {
		return time.Time{}, time.Time{}, false
	}

	// The 10ms increment is applied after validation because it can legitimately
	// roll the reading past midnight into the following day.
	if tenms > 0 && tenms <= maxTenMsIncrement {
		local = local.Add(time.Duration(tenms) * 10 * time.Millisecond)
	}

	return local.UTC(), local, offsetValid
}

// ModifiedTime returns the entry's last modification time in UTC, or the
// zero time if the volume records none. When the entry carries no UTC offset
// the stored wall clock is returned as-is; use Timestamps to tell the two
// cases apart.
func (e Entry) ModifiedTime() time.Time {
	utc, _, _ := decodeTimestamp(e.modified, e.modified10ms, e.modifiedUtcOffset)
	return utc
}

// CreatedTime returns the entry's creation time in UTC, or the zero time if
// the volume records none. This is also the entry's birth time: exFAT has no
// separate metadata-change timestamp.
func (e Entry) CreatedTime() time.Time {
	utc, _, _ := decodeTimestamp(e.created, e.created10ms, e.createdUtcOffset)
	return utc
}

// AccessedTime returns the entry's last access time in UTC, or the zero time
// if the volume records none. exFAT stores no sub-second precision for access,
// so this value is always on a two-second boundary.
func (e Entry) AccessedTime() time.Time {
	utc, _, _ := decodeTimestamp(e.accessed, 0, e.accessedUtcOffset)
	return utc
}

// Timestamps returns every timestamp on the entry together with the stored
// wall-clock readings and whether each was anchored by a recorded UTC offset.
func (e Entry) Timestamps() Timestamps {
	var t Timestamps
	t.Modified, t.ModifiedLocal, t.ModifiedOffsetValid = decodeTimestamp(e.modified, e.modified10ms, e.modifiedUtcOffset)
	t.Created, t.CreatedLocal, t.CreatedOffsetValid = decodeTimestamp(e.created, e.created10ms, e.createdUtcOffset)
	t.Accessed, t.AccessedLocal, t.AccessedOffsetValid = decodeTimestamp(e.accessed, 0, e.accessedUtcOffset)
	return t
}
