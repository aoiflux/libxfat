package libxfat

import (
	"fmt"
	"testing"
)

// Benchmarks for the parsing path with no I/O in the way, so what they measure
// is entry set assembly itself: the name buffer, the checksum accumulation and
// the per-entry allocations that go with them.
//
// The volume-level benchmarks, which do involve reads, live in tests/.

func benchDirData(b *testing.B, entries int) []byte {
	b.Helper()

	sets := make([][]byte, 0, entries)
	for i := 0; i < entries; i++ {
		sets = append(sets, buildEntrySet(testSet{
			name:    fmt.Sprintf("benchmark-file-%05d.bin", i),
			attrs:   ENTRY_ATTR_ATTR_MASK,
			cluster: uint32(5 + i%64),
			size:    1024,
		}))
	}
	return buildDir(sets...)
}

// BenchmarkParseDirChunk is the real shape of the work: a directory cluster in,
// a fresh slice of entries out. The result slice is part of what the caller
// asked for, so it is counted.
func BenchmarkParseDirChunk(b *testing.B) {
	for _, count := range []int{16, 128} {
		data := benchDirData(b, count)

		b.Run(fmt.Sprintf("entries=%d", count), func(b *testing.B) {
			parser := newTestParser(true)
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				var entries []Entry
				parser.resetDirParser()
				parser.parseDirChunk(data, &entries)
			}
		})
	}
}

// BenchmarkParseDirChunkReusedResult holds the result slice constant so that
// what remains is the parser's own overhead - the part Phase 1 targets, and the
// part a caller cannot avoid by managing their own buffers.
func BenchmarkParseDirChunkReusedResult(b *testing.B) {
	const count = 128
	data := benchDirData(b, count)

	parser := newTestParser(true)
	entries := make([]Entry, 0, count)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		entries = entries[:0]
		parser.resetDirParser()
		parser.parseDirChunk(data, &entries)
	}
}

// BenchmarkParseDeletedDirEntries covers the carving parser, which runs over
// every unallocated cluster on a volume and so multiplies whatever it costs.
func BenchmarkParseDeletedDirEntries(b *testing.B) {
	const count = 128

	sets := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		sets = append(sets, buildEntrySet(testSet{
			name:    fmt.Sprintf("erased-%05d.bin", i),
			attrs:   ENTRY_ATTR_ATTR_MASK,
			cluster: uint32(5 + i%64),
			size:    1024,
			deleted: true,
		}))
	}
	data := buildDir(sets...)

	parser := newTestParser(true)
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		parser.parseDeletedDirEntries(data)
	}
}

// BenchmarkChecksumAndName measures the two per-record helpers directly, since
// they run once per 32-byte record and dominate by call count.
func BenchmarkChecksumAndName(b *testing.B) {
	rec := make([]byte, EXFAT_DIRRECORD_SIZE)
	for i := range rec {
		rec[i] = byte(i * 7)
	}
	rec[0] = EXFAT_DIRRECORD_FILENAME_EXT

	b.Run("checksum", func(b *testing.B) {
		b.ReportAllocs()
		var accum uint16
		for i := 0; i < b.N; i++ {
			accum = exfatDirSetChecksumAdd(accum, rec, false)
		}
		runtimeSink16 = accum
	})

	// Both decode helpers are measured as the parser uses them: appending into
	// a buffer that is reused across records, not returning a fresh slice.
	b.Run("appendUTF16LEUnits", func(b *testing.B) {
		units := make([]uint16, 0, 15)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			units = appendUTF16LEUnits(units[:0], rec[2:EXFAT_DIRRECORD_SIZE], 15)
		}
		runtimeSinkUnits = units
	})

	b.Run("appendUTF16AsUTF8", func(b *testing.B) {
		units := appendUTF16LEUnits(nil, rec[2:EXFAT_DIRRECORD_SIZE], 15)
		buf := make([]byte, 0, 64)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			buf = appendUTF16AsUTF8(buf[:0], units)
		}
		runtimeSinkString = string(buf)
	})
}

// Sinks, so the compiler cannot delete the work being measured.
var (
	runtimeSink16     uint16
	runtimeSinkUnits  []uint16
	runtimeSinkString string
)
