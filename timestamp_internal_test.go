package libxfat

import (
	"encoding/binary"
	"testing"
	"time"
)

// packTimestamp builds an exFAT packed DOS timestamp.
func packTimestamp(year, month, day, hour, minute, second int) uint32 {
	return uint32(year-1980)<<25 |
		uint32(month)<<21 |
		uint32(day)<<16 |
		uint32(hour)<<11 |
		uint32(minute)<<5 |
		uint32(second/2)
}

// encodeUtcOffset encodes a UTC offset as exFAT stores it: bit 7 marks it
// valid, bits 0..6 are a signed count of 15-minute increments.
func encodeUtcOffset(d time.Duration) byte {
	units := int(d / (15 * time.Minute))
	return byte(utcOffsetValidMask | (units & utcOffsetValueMask))
}

func TestDecodeTimestampAppliesUtcOffset(t *testing.T) {
	// 2024-05-01 12:00:00 recorded at UTC-05:00 is 17:00:00 UTC.
	packed := packTimestamp(2024, 5, 1, 12, 0, 0)
	offset := encodeUtcOffset(-5 * time.Hour)

	if offset != 0xEC {
		t.Fatalf("encodeUtcOffset(-5h) = 0x%02X, want 0xEC", offset)
	}

	utc, local, valid := decodeTimestamp(packed, 0, offset)
	if !valid {
		t.Fatal("decodeTimestamp() offsetValid = false, want true")
	}

	want := time.Date(2024, 5, 1, 17, 0, 0, 0, time.UTC)
	if !utc.Equal(want) {
		t.Fatalf("decodeTimestamp() utc = %v, want %v", utc, want)
	}
	if h, m, s := local.Clock(); h != 12 || m != 0 || s != 0 {
		t.Fatalf("decodeTimestamp() local clock = %02d:%02d:%02d, want 12:00:00", h, m, s)
	}
	if _, secondsEast := local.Zone(); secondsEast != -5*3600 {
		t.Fatalf("decodeTimestamp() local zone = %d, want %d", secondsEast, -5*3600)
	}
}

func TestDecodeTimestampPositiveOffset(t *testing.T) {
	// 2023-01-02 09:30:00 at UTC+05:30 is 04:00:00 UTC.
	packed := packTimestamp(2023, 1, 2, 9, 30, 0)
	utc, _, valid := decodeTimestamp(packed, 0, encodeUtcOffset(5*time.Hour+30*time.Minute))
	if !valid {
		t.Fatal("decodeTimestamp() offsetValid = false, want true")
	}

	want := time.Date(2023, 1, 2, 4, 0, 0, 0, time.UTC)
	if !utc.Equal(want) {
		t.Fatalf("decodeTimestamp() utc = %v, want %v", utc, want)
	}
}

func TestDecodeTimestampWithoutOffsetAssumesUTC(t *testing.T) {
	packed := packTimestamp(2024, 5, 1, 12, 0, 0)

	utc, _, valid := decodeTimestamp(packed, 0, 0x00)
	if valid {
		t.Fatal("decodeTimestamp() offsetValid = true, want false")
	}

	want := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	if !utc.Equal(want) {
		t.Fatalf("decodeTimestamp() utc = %v, want %v", utc, want)
	}
}

func TestDecodeTimestampAppliesTenMillisecondIncrement(t *testing.T) {
	packed := packTimestamp(2024, 5, 1, 12, 0, 10)

	utc, _, _ := decodeTimestamp(packed, 199, 0x00)
	want := time.Date(2024, 5, 1, 12, 0, 11, 990*int(time.Millisecond), time.UTC)
	if !utc.Equal(want) {
		t.Fatalf("decodeTimestamp() utc = %v, want %v", utc, want)
	}
}

func TestDecodeTimestampZeroIsZeroTime(t *testing.T) {
	utc, local, valid := decodeTimestamp(0, 0, 0)
	if !utc.IsZero() || !local.IsZero() || valid {
		t.Fatalf("decodeTimestamp(0) = (%v, %v, %v), want zero times and false", utc, local, valid)
	}
}

// TestDecodeTimestampRejectsImpossibleDates guards against corrupt metadata
// being normalised into a confident-looking answer.
func TestDecodeTimestampRejectsImpossibleDates(t *testing.T) {
	cases := map[string]uint32{
		"month zero":     packTimestamp(2024, 0, 1, 12, 0, 0),
		"month thirteen": packTimestamp(2024, 13, 1, 12, 0, 0),
		"day zero":       packTimestamp(2024, 5, 0, 12, 0, 0),
		"february 31":    packTimestamp(2023, 2, 31, 12, 0, 0),
		"hour 24":        packTimestamp(2024, 5, 1, 24, 0, 0),
		"minute 60":      packTimestamp(2024, 5, 1, 12, 60, 0),
		"second 62":      packTimestamp(2024, 5, 1, 12, 0, 62),
	}

	for name, packed := range cases {
		t.Run(name, func(t *testing.T) {
			utc, _, _ := decodeTimestamp(packed, 0, 0)
			if !utc.IsZero() {
				t.Fatalf("decodeTimestamp(%s) = %v, want zero time", name, utc)
			}
		})
	}
}

// TestParseDirReadsTimestampsAndOffsets is the wiring test: it proves the
// parser reads the UTC offset bytes at 22/23/24 of the file directory entry and
// ValidDataLength at offset 8 of the stream extension entry.
func TestParseDirReadsTimestampsAndOffsets(t *testing.T) {
	exfat := newDirParser(&VBR{}, true, false)

	const (
		dataLength      = uint64(4096)
		validDataLength = uint64(1234)
		firstCluster    = uint32(9)
	)

	created := packTimestamp(2020, 1, 2, 3, 4, 6)
	modified := packTimestamp(2021, 6, 7, 8, 9, 10)
	accessed := packTimestamp(2022, 11, 12, 13, 14, 16)

	clusterdata := make([]byte, EXFAT_DIRRECORD_SIZE*3)

	// File directory entry.
	clusterdata[0] = EXFAT_DIRRECORD_FILEDIR
	clusterdata[1] = 2
	binary.LittleEndian.PutUint32(clusterdata[8:12], created)
	binary.LittleEndian.PutUint32(clusterdata[12:16], modified)
	binary.LittleEndian.PutUint32(clusterdata[16:20], accessed)
	clusterdata[20] = 100 // created 10ms increments
	clusterdata[21] = 50  // modified 10ms increments
	clusterdata[22] = encodeUtcOffset(-8 * time.Hour)
	clusterdata[23] = encodeUtcOffset(2 * time.Hour)
	clusterdata[24] = encodeUtcOffset(0)

	// Stream extension entry.
	stream := EXFAT_DIRRECORD_SIZE
	clusterdata[stream] = EXFAT_DIRRECORD_STREAM_EXT
	clusterdata[stream+3] = 2 // name length in UTF-16 units
	binary.LittleEndian.PutUint64(clusterdata[stream+8:stream+16], validDataLength)
	binary.LittleEndian.PutUint32(clusterdata[stream+20:stream+24], firstCluster)
	binary.LittleEndian.PutUint64(clusterdata[stream+24:stream+32], dataLength)

	// File name entry spelling "Hi".
	name := EXFAT_DIRRECORD_SIZE * 2
	clusterdata[name] = EXFAT_DIRRECORD_FILENAME_EXT
	clusterdata[name+2] = 'H'
	clusterdata[name+4] = 'i'

	// The validators bound lengths against the volume geometry.
	exfat.v.nbClusters = 64
	exfat.v.clusterSize = 512

	entries := exfat.parseDir(clusterdata)
	if len(entries) != 1 {
		t.Fatalf("parseDir() entries = %d, want 1", len(entries))
	}
	entry := entries[0]

	if got, want := entry.Name(), "Hi"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
	if got := entry.Size(); got != dataLength {
		t.Fatalf("Size() = %d, want %d", got, dataLength)
	}
	if got := entry.ValidDataSize(); got != validDataLength {
		t.Fatalf("ValidDataSize() = %d, want %d", got, validDataLength)
	}

	ts := entry.Timestamps()
	if !ts.CreatedOffsetValid || !ts.ModifiedOffsetValid || !ts.AccessedOffsetValid {
		t.Fatalf("offset validity = (%v, %v, %v), want all true",
			ts.CreatedOffsetValid, ts.ModifiedOffsetValid, ts.AccessedOffsetValid)
	}

	// 2020-01-02 03:04:06 +1.00s at UTC-08:00 -> 11:04:07 UTC
	wantCreated := time.Date(2020, 1, 2, 11, 4, 7, 0, time.UTC)
	if !entry.CreatedTime().Equal(wantCreated) {
		t.Fatalf("CreatedTime() = %v, want %v", entry.CreatedTime(), wantCreated)
	}

	// 2021-06-07 08:09:10 +0.50s at UTC+02:00 -> 06:09:10.5 UTC
	wantModified := time.Date(2021, 6, 7, 6, 9, 10, 500*int(time.Millisecond), time.UTC)
	if !entry.ModifiedTime().Equal(wantModified) {
		t.Fatalf("ModifiedTime() = %v, want %v", entry.ModifiedTime(), wantModified)
	}

	// Access has no sub-second field and this one is recorded at UTC+00:00.
	wantAccessed := time.Date(2022, 11, 12, 13, 14, 16, 0, time.UTC)
	if !entry.AccessedTime().Equal(wantAccessed) {
		t.Fatalf("AccessedTime() = %v, want %v", entry.AccessedTime(), wantAccessed)
	}
}

// TestDeletedEntriesCarryTimestamps confirms recovered entries get the same
// treatment, since deleted-entry recovery shares the record parser.
func TestDeletedEntriesCarryTimestamps(t *testing.T) {
	exfat := newDirParser(&VBR{nbClusters: 64, clusterSize: 512}, true, false)

	modified := packTimestamp(2019, 3, 4, 5, 6, 8)

	clusterdata := make([]byte, EXFAT_DIRRECORD_SIZE*3)
	clusterdata[0] = EXFAT_DIRRECORD_DEL_FILEDIR
	clusterdata[1] = 2
	binary.LittleEndian.PutUint32(clusterdata[12:16], modified)
	clusterdata[23] = encodeUtcOffset(time.Hour)

	stream := EXFAT_DIRRECORD_SIZE
	clusterdata[stream] = EXFAT_DIRRECORD_DEL_STREAM_EXT
	clusterdata[stream+3] = 1

	name := EXFAT_DIRRECORD_SIZE * 2
	clusterdata[name] = EXFAT_DIRRECORD_DEL_FILENAME_EXT
	clusterdata[name+2] = 'X'

	entries := exfat.parseDeletedDirEntries(unlocatedChunk, clusterdata)
	if len(entries) != 1 {
		t.Fatalf("parseDeletedDirEntries() entries = %d, want 1", len(entries))
	}

	want := time.Date(2019, 3, 4, 4, 6, 8, 0, time.UTC)
	if got := entries[0].ModifiedTime(); !got.Equal(want) {
		t.Fatalf("ModifiedTime() = %v, want %v", got, want)
	}
}
