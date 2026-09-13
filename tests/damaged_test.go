package test

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoiflux/libxfat/v2"
)

// Clusters used only by the damaged image. Every one of them is past
// sfLastUsedCluster and past the chains listed in sfExtraChains, so adding them
// disturbs nothing the superfloppy tests assert against.
const (
	dmBrokenFirst  = 31 // chains 31 -> 33, and 33's own FAT entry is free
	dmBrokenSecond = 33
	dmLoopFirst    = 35 // chains 35 -> 36 -> 35
	dmLoopSecond   = 36
	dmDeletedFree  = 38 // deleted; its first cluster is still free
	dmDeletedTaken = 44 // deleted; its first cluster has been taken again
	dmDeletedNoFat = 48 // deleted, and the surviving stream extension says NoFatChain
	dmNoAllocFirst = 50 // live and chained 50 -> 51 -> 52, but its record says no
	dmNoAllocMid   = 51 // allocation is possible at all
	dmNoAllocLast  = 52

	// dmSize is three clusters' worth of bytes less a trim, so a result that
	// locates fewer than three clusters is visibly truncated and one that locates
	// all three is visibly trimmed to the recorded size.
	dmSize     = 10000
	dmClusters = 3
)

// buildDamagedImage returns the superfloppy volume with broken chains and deleted
// entries added.
//
// It is a separate builder rather than more cases in buildSuperfloppyImage
// because that fixture's stated purpose is to be conformant enough for a
// third-party checker to bless it, and a free FAT entry in the middle of a live
// file's chain forfeits exactly that. Damage belongs on a copy no checker is
// handed.
func buildDamagedImage(t *testing.T) []byte {
	t.Helper()

	image := buildSuperfloppyImage()

	fat := image[sfFatOffsetSector*sfSectorSize : (sfFatOffsetSector+sfFatSizeSectors)*sfSectorSize]
	setFat := func(cluster, next uint32) {
		binary.LittleEndian.PutUint32(fat[cluster*4:cluster*4+4], next)
	}
	clusterAt := func(cluster uint32) []byte {
		start := sfHeapOffsetSector*sfSectorSize + int(cluster-2)*sfClusterSize
		return image[start : start+sfClusterSize]
	}
	bitmap := clusterAt(sfBitmapCluster)
	markAllocated := func(cluster uint32) {
		index := cluster - 2
		bitmap[index/8] |= 1 << (index % 8)
	}

	// A chain that ends on a free entry instead of an end-of-chain marker. Both
	// clusters are genuinely allocated, so the only thing wrong is the FAT - which
	// is what makes the located prefix worth keeping.
	setFat(dmBrokenFirst, dmBrokenSecond)
	setFat(dmBrokenSecond, 0x00000000)
	markAllocated(dmBrokenFirst)
	markAllocated(dmBrokenSecond)

	// A live, allocated, perfectly ordinary three-cluster chain whose stream
	// extension leaves AllocationPossible clear. The specification defines
	// FirstCluster and DataLength as undefined when that bit is clear, so the record
	// contradicts itself; nothing else about it is wrong, which is what makes it the
	// case worth pinning - the ranges are locatable and the contradiction has to be
	// reported alongside them rather than instead of them.
	setFat(dmNoAllocFirst, dmNoAllocMid)
	setFat(dmNoAllocMid, dmNoAllocLast)
	setFat(dmNoAllocLast, 0xffffffff)
	markAllocated(dmNoAllocFirst)
	markAllocated(dmNoAllocMid)
	markAllocated(dmNoAllocLast)

	// A chain that returns to where it started.
	setFat(dmLoopFirst, dmLoopSecond)
	setFat(dmLoopSecond, dmLoopFirst)
	markAllocated(dmLoopFirst)
	markAllocated(dmLoopSecond)

	// The deleted entries get no FAT entries at all: deletion frees the chain,
	// which is the whole reason it must not be walked. Marking dmDeletedTaken's
	// first cluster in use stands for a later file having claimed it.
	markAllocated(dmDeletedTaken)

	// Append to the root. The first record whose type byte is zero is
	// end-of-directory, so that is where these sets go. Stepping one record at a
	// time rather than by SecondaryCount matters: in the benign primary records
	// this root starts with, byte 1 is a character count or reserved, not a count
	// of secondaries.
	root := clusterAt(sfRootCluster)
	offset := 0
	for offset < len(root) && root[offset] != 0x00 {
		offset += 32
	}

	appendSet := func(s sfEntrySet) {
		records := sfBuildEntrySet(s)
		if offset+len(records) > len(root) {
			t.Fatalf("damaged root overflows its cluster at %q", s.name)
		}
		copy(root[offset:], records)
		offset += len(records)
	}

	appendSet(sfEntrySet{
		name: "broken.bin", attrs: 0x20, cluster: dmBrokenFirst, size: dmSize,
	})
	appendSet(sfEntrySet{
		name: "looped.bin", attrs: 0x20, cluster: dmLoopFirst, size: dmSize,
	})
	appendSet(sfEntrySet{
		name: "deleted-free.bin", attrs: 0x20, cluster: dmDeletedFree, size: dmSize,
		deleted: true,
	})
	appendSet(sfEntrySet{
		name: "deleted-taken.bin", attrs: 0x20, cluster: dmDeletedTaken, size: dmSize,
		deleted: true,
	})
	appendSet(sfEntrySet{
		name: "deleted-nofatchain.bin", attrs: 0x20, cluster: dmDeletedNoFat, size: dmSize,
		noFatChain: true, deleted: true,
	})
	appendSet(sfEntrySet{
		name: "no-allocation.bin", attrs: 0x20, cluster: dmNoAllocFirst, size: dmSize,
		noAllocation: true,
	})

	return image
}

func openDamaged(t *testing.T) *libxfat.ExFAT {
	t.Helper()

	image := buildDamagedImage(t)
	path := filepath.Join(t.TempDir(), "damaged.exfat")
	if err := os.WriteFile(path, image, 0o600); err != nil {
		t.Fatalf("write damaged fixture: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open damaged fixture: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })

	fs, err := libxfat.Open(libxfat.Source{
		Reader:                file,
		Size:                  int64(len(image)),
		Strict:                true,
		IgnorePartitionOffset: true,
	})
	if err != nil {
		t.Fatalf("open damaged volume: %v", err)
	}
	return fs
}

// located describes a Range by cluster, which is what these assertions are
// actually about; the byte offsets are derived from it and checked alongside.
type located struct {
	cluster uint32
	count   uint32
	length  int64
}

func assertRanges(t *testing.T, fs *libxfat.ExFAT, got []libxfat.Range, want []located) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("got %d ranges, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].StartCluster != w.cluster || got[i].ClusterCount != w.count {
			t.Errorf("range %d: clusters %d+%d, want %d+%d",
				i, got[i].StartCluster, got[i].ClusterCount, w.cluster, w.count)
		}
		if got[i].Length != w.length {
			t.Errorf("range %d: length %d, want %d", i, got[i].Length, w.length)
		}
		if start := int64(mustClusterOffset(t, fs, w.cluster)); got[i].StartByte != start {
			t.Errorf("range %d: StartByte %d, want cluster %d at %d",
				i, got[i].StartByte, w.cluster, start)
		}
	}
}

// TestDamagedBrokenChainKeepsThePrefix pins the split the library is built
// around: the cluster API refuses a chain that does not end properly, while the
// extent API hands back every run it did establish and says why it stopped.
func TestDamagedBrokenChainKeepsThePrefix(t *testing.T) {
	fs := openDamaged(t)
	entry := entryNamed(t, fs, "broken.bin")

	result, err := fs.FragmentOffsetsWithOptions(entry, libxfat.FragmentOptions{})
	if err != nil {
		t.Fatalf("FragmentOffsetsWithOptions: %v", err)
	}

	assertRanges(t, fs, result.Ranges, []located{
		{cluster: dmBrokenFirst, count: 1, length: sfClusterSize},
		{cluster: dmBrokenSecond, count: 1, length: sfClusterSize},
	})
	if !result.ChainBroken {
		t.Error("ChainBroken is false for a chain that stopped on a free entry")
	}
	if result.LoopDetected {
		t.Error("LoopDetected is true for a chain that never repeated a cluster")
	}
	if !result.ChainWalked {
		t.Error("ChainWalked is false, but these runs came from the FAT")
	}
	if !result.Truncated {
		t.Errorf("Truncated is false: located %d of %d recorded bytes",
			result.BytesCovered, dmSize)
	}
	if want := int64(2 * sfClusterSize); result.BytesCovered != want {
		t.Errorf("BytesCovered = %d, want %d", result.BytesCovered, want)
	}

	// The same entry through the cluster API. A partial cluster list is a list
	// that does not describe the file, so it is an error there and a flag here.
	if _, _, err := fs.ClusterList(entry); err == nil {
		t.Error("ClusterList returned no error for a broken chain")
	}
}

// TestDamagedLoopedChainKeepsThePrefix is the same contract for a chain that
// revisits a cluster, where the cluster API's error is pinned to a sentinel.
func TestDamagedLoopedChainKeepsThePrefix(t *testing.T) {
	fs := openDamaged(t)
	entry := entryNamed(t, fs, "looped.bin")

	result, err := fs.FragmentOffsetsWithOptions(entry, libxfat.FragmentOptions{})
	if err != nil {
		t.Fatalf("FragmentOffsetsWithOptions: %v", err)
	}

	// 35 and 36 are consecutive, so the two located clusters are one run.
	assertRanges(t, fs, result.Ranges, []located{
		{cluster: dmLoopFirst, count: 2, length: 2 * sfClusterSize},
	})
	if !result.LoopDetected {
		t.Error("LoopDetected is false for a chain that returned to its first cluster")
	}
	if result.ChainBroken {
		t.Error("ChainBroken is true for a chain that stopped on a repeat, not a break")
	}
	if !result.Truncated {
		t.Error("Truncated is false for a chain that stopped short of the recorded size")
	}

	_, _, err = fs.ClusterList(entry)
	if !errors.Is(err, libxfat.ErrClusterChainLoop) {
		t.Errorf("ClusterList error = %v, want ErrClusterChainLoop", err)
	}
}

// TestDamagedDeletedEntryLocatesOnlyItsFirstCluster covers the default: a deleted
// entry's chain is freed, so nothing past the cluster the record still names can
// be established, and the library does not guess unless it is asked to.
func TestDamagedDeletedEntryLocatesOnlyItsFirstCluster(t *testing.T) {
	fs := openDamaged(t)
	entry := entryNamed(t, fs, "deleted-free.bin")
	if !entry.IsDeleted() {
		t.Fatal("fixture entry deleted-free.bin is not marked deleted")
	}

	result, err := fs.FragmentOffsetsWithOptions(entry, libxfat.FragmentOptions{})
	if err != nil {
		t.Fatalf("FragmentOffsetsWithOptions: %v", err)
	}

	assertRanges(t, fs, result.Ranges, []located{
		{cluster: dmDeletedFree, count: 1, length: sfClusterSize},
	})
	switch {
	case result.ChainWalked:
		t.Error("ChainWalked is true for a deleted entry, whose chain must not be walked")
	case result.Assumed:
		t.Error("Assumed is true under the zero-value options, which assume nothing")
	case result.FirstClusterReallocated:
		t.Error("FirstClusterReallocated is true for a cluster the bitmap marks free")
	case !result.Truncated:
		t.Error("Truncated is false, though only one of three clusters was located")
	}

	// Asked for a hypothesis, the library supplies one and labels it as such.
	assumed, err := fs.FragmentOffsetsWithOptions(entry,
		libxfat.FragmentOptions{AssumeContiguous: true})
	if err != nil {
		t.Fatalf("FragmentOffsetsWithOptions(AssumeContiguous): %v", err)
	}

	assertRanges(t, fs, assumed.Ranges, []located{
		{cluster: dmDeletedFree, count: dmClusters, length: dmSize},
	})
	if !assumed.Assumed {
		t.Error("Assumed is false for ranges no FAT entry and no record supports")
	}
	if assumed.Truncated {
		t.Error("Truncated is true although the assumed runs cover the recorded size")
	}
}

// TestDamagedDeletedEntryReportsAReallocatedFirstCluster is the difference
// between a deleted file that might still be recoverable and one whose bytes have
// probably been overwritten - a distinction the ranges alone cannot carry.
func TestDamagedDeletedEntryReportsAReallocatedFirstCluster(t *testing.T) {
	fs := openDamaged(t)

	for _, tc := range []struct {
		name string
		want bool
	}{
		{name: "deleted-free.bin", want: false},
		{name: "deleted-taken.bin", want: true},
	} {
		entry := entryNamed(t, fs, tc.name)
		result, err := fs.FragmentOffsetsWithOptions(entry, libxfat.FragmentOptions{})
		if err != nil {
			t.Fatalf("%s: FragmentOffsetsWithOptions: %v", tc.name, err)
		}
		if result.FirstClusterReallocated != tc.want {
			t.Errorf("%s: FirstClusterReallocated = %v, want %v",
				tc.name, result.FirstClusterReallocated, tc.want)
		}
	}
}

// TestDamagedDeletedNoFatChainNeedsNoAssumption is the deliberate divergence from
// the sibling FAT library. A deleted exFAT entry whose stream extension recorded
// NoFatChain has the volume's own word that the allocation was contiguous, and
// deletion does not erase that record. Reporting it as a guess would throw away
// the one advantage exFAT has here.
func TestDamagedDeletedNoFatChainNeedsNoAssumption(t *testing.T) {
	fs := openDamaged(t)
	entry := entryNamed(t, fs, "deleted-nofatchain.bin")
	if !entry.IsDeleted() || !entry.IsContiguous() {
		t.Fatalf("fixture entry is deleted=%v contiguous=%v, want both true",
			entry.IsDeleted(), entry.IsContiguous())
	}

	// No AssumeContiguous, and yet the whole allocation is located.
	result, err := fs.FragmentOffsetsWithOptions(entry, libxfat.FragmentOptions{})
	if err != nil {
		t.Fatalf("FragmentOffsetsWithOptions: %v", err)
	}

	assertRanges(t, fs, result.Ranges, []located{
		{cluster: dmDeletedNoFat, count: dmClusters, length: dmSize},
	})
	switch {
	case result.Assumed:
		t.Error("Assumed is true for a run the volume itself declared contiguous")
	case !result.NoFatChain:
		t.Error("NoFatChain is false although the stream extension set the flag")
	case result.ChainWalked:
		t.Error("ChainWalked is true although no FAT entry was read")
	case result.Truncated:
		t.Error("Truncated is true although the run covers the recorded size")
	}
}

// TestDamagedRecordDeclaringNoAllocationIsFlagged covers a record that names a
// first cluster while its own GeneralSecondaryFlags say no allocation is possible.
//
// The specification defines FirstCluster and DataLength as undefined when that bit
// is clear, so the record contradicts itself. The library's answer is the one it
// gives everywhere else: locate what the fields point at, and say that the fields
// were not to be believed. Before this flag existed the contradiction was invisible
// - only bit 1 was ever read - and these ranges were indistinguishable from ranges
// located out of a coherent record.
func TestDamagedRecordDeclaringNoAllocationIsFlagged(t *testing.T) {
	fs := openDamaged(t)
	entry := entryNamed(t, fs, "no-allocation.bin")

	if entry.AllocationPossible() {
		t.Error("AllocationPossible is true for a record that left bit 0 clear")
	}
	if got := entry.SecondaryFlags(); got != 0 {
		t.Errorf("SecondaryFlags = %#02x, want 0x00", got)
	}
	if entry.IsContiguous() {
		t.Error("IsContiguous is true, but no NoFatChain bit was written either")
	}

	result, err := fs.FragmentOffsetsWithOptions(entry, libxfat.FragmentOptions{})
	if err != nil {
		t.Fatalf("FragmentOffsetsWithOptions: %v", err)
	}
	if !result.AllocationContradiction {
		t.Error("AllocationContradiction is false for a self-contradictory record")
	}

	// The chain is intact and the clusters are consecutive, so the contradiction is
	// the only thing wrong: one run, trimmed to the recorded size, from the FAT.
	assertRanges(t, fs, result.Ranges, []located{
		{cluster: dmNoAllocFirst, count: dmClusters, length: dmSize},
	})
	if !result.ChainWalked {
		t.Error("ChainWalked is false, but these runs came from the FAT")
	}
	for name, flag := range map[string]bool{
		"Truncated":    result.Truncated,
		"ChainBroken":  result.ChainBroken,
		"LoopDetected": result.LoopDetected,
		"Assumed":      result.Assumed,
		"NoFatChain":   result.NoFatChain,
	} {
		if flag {
			t.Errorf("%s is true for an intact chain", name)
		}
	}
}

// TestDamagedOrdinaryRecordIsNotFlagged is the other half: the contradiction must
// not fire on the records every volume is full of, or it says nothing.
func TestDamagedOrdinaryRecordIsNotFlagged(t *testing.T) {
	fs := openDamaged(t)

	for _, name := range []string{"broken.bin", "looped.bin", "readme.txt", "fragmented.bin"} {
		entry := entryNamed(t, fs, name)
		if !entry.AllocationPossible() {
			t.Errorf("%s: AllocationPossible is false for a conformant record", name)
		}

		result, err := fs.FragmentOffsetsWithOptions(entry, libxfat.FragmentOptions{})
		if err != nil {
			t.Fatalf("%s: FragmentOffsetsWithOptions: %v", name, err)
		}
		if result.AllocationContradiction {
			t.Errorf("%s: AllocationContradiction is true for a conformant record", name)
		}
	}
}

// TestDamagedMetadataStreamsClaimTheirAllocation covers the entries whose records
// have no GeneralSecondaryFlags field at all. $BitMap and $UpCase state their
// allocation through their own FirstCluster and DataLength, and the synthetic
// region entries are byte ranges the library invented; neither may be reported as
// contradicting a flag it never carried.
func TestDamagedMetadataStreamsClaimTheirAllocation(t *testing.T) {
	fs := openDamaged(t)

	for _, name := range []string{"$BitMap", "$UpCase", "$MBR", "$FAT1"} {
		entry := entryNamed(t, fs, name)
		if !entry.AllocationPossible() {
			t.Errorf("%s: AllocationPossible is false, but the stream has an allocation", name)
		}

		result, err := fs.FragmentOffsetsWithOptions(entry, libxfat.FragmentOptions{})
		if err != nil {
			t.Fatalf("%s: FragmentOffsetsWithOptions: %v", name, err)
		}
		if result.AllocationContradiction {
			t.Errorf("%s: AllocationContradiction is true for a record with no such flag", name)
		}
	}
}
