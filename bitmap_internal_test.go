package libxfat

import "testing"

// The allocation bitmap is read in cluster-sized chunks, so bitmapCounter has to
// carry a partial 4-byte word across chunk boundaries. That carry is the whole
// reason the type exists, and how the volume reports its free space depends on
// it, yet the boundary cases were never exercised: a chunking bug would quietly
// misreport how full a volume is, in a direction nothing else would contradict.

// popcount counts set bits directly, independently of the packing that
// bitmapCounter does. The count must not depend on how the bytes were grouped.
func popcount(data []byte) uint32 {
	var total uint32
	for _, b := range data {
		for bit := 0; bit < 8; bit++ {
			if b&(1<<bit) != 0 {
				total++
			}
		}
	}
	return total
}

func TestBitmapCounterIsIndependentOfChunking(t *testing.T) {
	// A pattern with set bits in every byte position and every alignment.
	data := make([]byte, 61) // deliberately not a multiple of 4
	state := uint32(0x12345678)
	for i := range data {
		state = state*1664525 + 1013904223
		data[i] = byte(state >> 24)
	}

	want := popcount(data)

	if got := countBitmap(data); got != want {
		t.Fatalf("countBitmap = %d, want %d", got, want)
	}

	// Every fixed chunk size, including sizes that split the 4-byte word at
	// each of its four offsets.
	for size := 1; size <= len(data)+1; size++ {
		counter := bitmapCounter{}
		for offset := 0; offset < len(data); offset += size {
			end := offset + size
			if end > len(data) {
				end = len(data)
			}
			counter.write(data[offset:end])
		}
		if got := counter.count(); got != want {
			t.Errorf("chunk size %d: count = %d, want %d", size, got, want)
		}
	}
}

func TestBitmapCounterHandlesRaggedChunks(t *testing.T) {
	data := make([]byte, 37)
	for i := range data {
		data[i] = 0xFF // 8 bits per byte, so the expected total is obvious
	}
	want := uint32(len(data) * 8)

	// Uneven chunk sequences, including several that leave a 1-, 2- and
	// 3-byte tail pending across a call.
	for _, sizes := range [][]int{
		{1, 3, 5, 7, 11, 10},
		{2, 2, 2, 31},
		{3, 1, 3, 1, 29},
		{36, 1},
		{1, 36},
		{37},
	} {
		counter := bitmapCounter{}
		offset := 0
		for _, size := range sizes {
			end := offset + size
			if end > len(data) {
				end = len(data)
			}
			counter.write(data[offset:end])
			offset = end
		}
		if offset != len(data) {
			t.Fatalf("chunk sizes %v cover %d of %d bytes", sizes, offset, len(data))
		}
		if got := counter.count(); got != want {
			t.Errorf("chunk sizes %v: count = %d, want %d", sizes, got, want)
		}
	}
}

// TestBitmapCounterIgnoresStaleTailBytes pins a hazard in the carry buffer: it
// is never cleared, so a short tail sits in front of whatever a longer earlier
// tail left behind. Only the first tailLen bytes may be counted.
func TestBitmapCounterIgnoresStaleTailBytes(t *testing.T) {
	counter := bitmapCounter{}

	// A 3-byte tail of set bits, completed and counted.
	counter.write([]byte{0xFF, 0xFF, 0xFF})
	counter.write([]byte{0xFF})
	if got := counter.count(); got != 32 {
		t.Fatalf("after four set bytes, count = %d, want 32", got)
	}

	// Now a single zero byte. If the three stale 0xFF bytes behind it were
	// counted, this would read as 32 rather than 0 extra bits.
	counter.write([]byte{0x00})
	if got := counter.count(); got != 32 {
		t.Fatalf("a zero byte added %d bits; stale tail bytes are being counted", got-32)
	}
}

func TestBitmapCounterEmptyAndZeroInput(t *testing.T) {
	counter := bitmapCounter{}
	if got := counter.count(); got != 0 {
		t.Errorf("empty counter = %d, want 0", got)
	}

	counter.write(nil)
	counter.write([]byte{})
	counter.write(make([]byte, 16))
	if got := counter.count(); got != 0 {
		t.Errorf("after zeroed input, count = %d, want 0", got)
	}
}
