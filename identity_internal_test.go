package libxfat

import (
	"fmt"
	"testing"
)

// fillDirCluster builds a directory cluster packed edge to edge with entry sets
// and no terminating zero record, which is what every cluster of a multi-cluster
// directory except the last one looks like. A zero-padded chunk would instead end
// the directory, since a zeroed record is how exFAT says there is nothing further.
//
// Names are 16 code units so that each set is exactly four records, which divides
// a 4096-byte cluster evenly; distinct so a misparse cannot hide behind a
// repeated name.
func fillDirCluster(t *testing.T, clusterSize int) (chunk []byte, setSize int, sets int) {
	t.Helper()

	for i := 0; ; i++ {
		set := buildEntrySet(testSet{
			name:    fmt.Sprintf("name-%011d", i),
			cluster: uint32(100 + i),
			size:    uint64(10 + i),
		})
		if setSize == 0 {
			setSize = len(set)
			if clusterSize%setSize != 0 {
				t.Fatalf("entry set of %d bytes does not divide a %d-byte cluster",
					setSize, clusterSize)
			}
		}
		chunk = append(chunk, set...)
		if len(chunk) == clusterSize {
			return chunk, setSize, i + 1
		}
	}
}

// TestIdentityAddressesAFragmentedDirectory is the case no fixture on disk
// reaches: a directory whose second cluster is not the one after its first.
//
// Deriving the offset from the chunk's own cluster makes this correct by
// construction, where deriving it from the directory's first cluster plus a
// running byte count - the obvious implementation - would place every entry in
// the second chunk 4096 bytes before where it actually is.
func TestIdentityAddressesAFragmentedDirectory(t *testing.T) {
	const (
		dirFirstCluster = 30
		secondCluster   = 47 // deliberately not 31
		clusterSize     = 4096
		slotsPerCluster = clusterSize / EXFAT_DIRRECORD_SIZE
	)

	firstChunk, setSize, setsInFirst := fillDirCluster(t, clusterSize)
	tail := buildEntrySet(testSet{name: "tail.txt", cluster: 200, size: 30})

	var entries []Entry
	parser := newTestParser(true)
	parser.inDirectory(dirFirstCluster)
	if parser.parseDirChunk(dirFirstCluster, firstChunk, &entries) {
		t.Fatal("a cluster packed full of entry sets reported the end of the directory")
	}
	parser.parseDirChunk(secondCluster, buildDir(tail), &entries)

	if want := setsInFirst + 1; len(entries) != want {
		t.Fatalf("parsed %d entries, want %d", len(entries), want)
	}

	// The slot index is logical: it counts slots from the start of the directory,
	// so the first entry of the second cluster follows the last slot of the first
	// one regardless of where that cluster physically is.
	last := entries[len(entries)-1]
	if got := uint32(slotsPerCluster); last.EntrySlotIndex() != got {
		t.Errorf("%q: slot index = %d, want %d",
			last.Name(), last.EntrySlotIndex(), got)
	}
	if got := entries[setsInFirst-1].EntrySlotIndex(); got != uint32(slotsPerCluster-setSize/EXFAT_DIRRECORD_SIZE) {
		t.Errorf("last entry of the first cluster: slot index = %d, want %d",
			got, slotsPerCluster-setSize/EXFAT_DIRRECORD_SIZE)
	}

	// Every entry names the directory, not the cluster it happened to be in.
	for _, entry := range entries {
		if got := entry.ParentFirstCluster(); got != dirFirstCluster {
			t.Errorf("%q: parent cluster = %d, want %d",
				entry.Name(), got, dirFirstCluster)
		}
	}

	// The offsets, by contrast, follow the clusters the chunks actually came from.
	// This is the assertion that fails if the offset is computed from the
	// directory's first cluster plus a running byte count.
	secondBase, err := safeInt64(parser.v.getClusterOffset(secondCluster))
	if err != nil {
		t.Fatalf("cluster %d offset: %v", secondCluster, err)
	}
	got, ok := last.EntrySetOffset()
	if !ok {
		t.Fatalf("%q reports no entry-set offset", last.Name())
	}
	if got != secondBase {
		t.Errorf("%q: offset = %d, want %d (cluster %d, not %d)",
			last.Name(), got, secondBase, secondCluster, dirFirstCluster+1)
	}
}

// TestIdentitySlotIndexCountsWithinTheChunk checks the within-chunk arithmetic,
// which the fragmented case above cannot see because both its entry sets sit in
// slot 0.
func TestIdentitySlotIndexCountsWithinTheChunk(t *testing.T) {
	first := buildEntrySet(testSet{name: "a.txt", cluster: 100, size: 10})
	second := buildEntrySet(testSet{name: "b.txt", cluster: 101, size: 20})

	var entries []Entry
	parser := newTestParser(true)
	parser.inDirectory(30)
	parser.parseDirChunk(30, buildDir(first, second), &entries)

	if len(entries) != 2 {
		t.Fatalf("parsed %d entries, want 2: %v", len(entries), entryNames(entries))
	}
	if got := entries[0].EntrySlotIndex(); got != 0 {
		t.Errorf("first entry: slot index = %d, want 0", got)
	}
	// The first set occupies len(first)/32 slots, so the second begins there.
	if want, got := uint32(len(first)/EXFAT_DIRRECORD_SIZE), entries[1].EntrySlotIndex(); got != want {
		t.Errorf("second entry: slot index = %d, want %d", got, want)
	}
}

// TestIdentityUnlocatedChunkReportsNoOffset pins the refusal. A parse of a buffer
// the caller could not place must not report an offset of zero as though zero
// were an address, because zero is the start of the image.
func TestIdentityUnlocatedChunkReportsNoOffset(t *testing.T) {
	set := buildEntrySet(testSet{name: "nowhere.txt", cluster: 100, size: 10})

	entries := newTestParser(true).parseDir(buildDir(set))
	if len(entries) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(entries))
	}
	if offset, ok := entries[0].EntrySetOffset(); ok {
		t.Errorf("unlocated chunk reported entry-set offset %d, want no offset", offset)
	}
}
