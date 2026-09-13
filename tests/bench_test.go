package test

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"

	"github.com/aoiflux/libxfat/v2"
)

// Volume-level benchmarks: the paths that involve reads, and whose cost scales
// with the number of entries or the number of unallocated clusters.
//
// The image is built for scale rather than for realism - the conformant,
// fsck-audited fixture is buildSuperfloppyImage, and it is deliberately small.
// This one reuses the same entry set and checksum helpers so the two cannot
// drift apart.

const (
	bnSectorSize      = 512
	bnSectorsPerClust = 8
	bnClusterSize     = bnSectorSize * bnSectorsPerClust
	bnFatOffsetSector = 128
	bnFatSizeSectors  = 8
	bnHeapOffset      = bnFatOffsetSector + bnFatSizeSectors
	// Unallocated clusters left for the carving path to scan.
	bnFreeClusters = 64
)

// buildBenchImage lays out a volume whose root directory spans as many clusters
// as fileCount requires, followed by a run of unallocated clusters holding
// deleted entry sets.
func buildBenchImage(fileCount int) []byte {
	// Three metadata records, three records per file, one terminator.
	rootBytes := (3 + fileCount*3 + 1) * 32
	rootClusters := (rootBytes + bnClusterSize - 1) / bnClusterSize

	bitmapCluster := uint32(2 + rootClusters)
	upcaseCluster := bitmapCluster + 1
	firstFree := upcaseCluster + 1
	clusterCount := rootClusters + 2 + bnFreeClusters

	heapSectors := clusterCount * bnSectorsPerClust
	volumeSectors := bnHeapOffset + heapSectors
	image := make([]byte, volumeSectors*bnSectorSize)

	// --- Boot region ---
	vbr := image[0 : 12*bnSectorSize]
	copy(vbr[3:11], []byte("EXFAT   "))
	binary.LittleEndian.PutUint64(vbr[0x48:0x50], uint64(volumeSectors))
	binary.LittleEndian.PutUint32(vbr[0x50:0x54], bnFatOffsetSector)
	binary.LittleEndian.PutUint32(vbr[0x54:0x58], bnFatSizeSectors)
	binary.LittleEndian.PutUint32(vbr[0x58:0x5c], bnHeapOffset)
	binary.LittleEndian.PutUint32(vbr[0x5c:0x60], uint32(clusterCount))
	binary.LittleEndian.PutUint32(vbr[0x60:0x64], 2)
	binary.LittleEndian.PutUint16(vbr[0x68:0x6a], 0x0100)
	vbr[0x6c] = 9
	vbr[0x6d] = 3
	vbr[0x6e] = 1
	vbr[0x70] = 50
	binary.BigEndian.PutUint16(vbr[0x1fe:0x200], 0x55aa)
	for sector := 1; sector <= 8; sector++ {
		end := (sector + 1) * bnSectorSize
		binary.LittleEndian.PutUint32(image[end-4:end], 0xAA550000)
	}
	checksum := sfBootChecksum(image[0 : 11*bnSectorSize])
	for off := 11 * bnSectorSize; off < 12*bnSectorSize; off += 4 {
		binary.LittleEndian.PutUint32(image[off:off+4], checksum)
	}

	fat := image[bnFatOffsetSector*bnSectorSize:]
	setFat := func(cluster, next uint32) {
		binary.LittleEndian.PutUint32(fat[cluster*4:cluster*4+4], next)
	}
	binary.LittleEndian.PutUint32(fat[0:4], 0xfffffff8)
	binary.LittleEndian.PutUint32(fat[4:8], 0xffffffff)

	clusterAt := func(cluster uint32) []byte {
		start := bnHeapOffset*bnSectorSize + int(cluster-2)*bnClusterSize
		return image[start : start+bnClusterSize]
	}

	// --- Root directory, chained across rootClusters ---
	for i := 0; i < rootClusters; i++ {
		cluster := uint32(2 + i)
		if i == rootClusters-1 {
			setFat(cluster, 0xffffffff)
		} else {
			setFat(cluster, cluster+1)
		}
	}
	setFat(bitmapCluster, 0xffffffff)
	setFat(upcaseCluster, 0xffffffff)

	root := make([]byte, rootClusters*bnClusterSize)
	offset := 0
	put := func(records []byte) {
		copy(root[offset:], records)
		offset += len(records)
	}

	label := make([]byte, 32)
	label[0] = 0x83
	labelUnits := utf16.Encode([]rune("BENCH"))
	label[1] = byte(len(labelUnits))
	for i, u := range labelUnits {
		binary.LittleEndian.PutUint16(label[2+i*2:4+i*2], u)
	}
	put(label)

	upcase := clusterAt(upcaseCluster)
	for code := 0; code < 256; code++ {
		mapped := uint16(code)
		if code >= 'a' && code <= 'z' {
			mapped = uint16(code - 0x20)
		}
		binary.LittleEndian.PutUint16(upcase[code*2:code*2+2], mapped)
	}

	bitmapEntry := make([]byte, 32)
	bitmapEntry[0] = 0x81
	binary.LittleEndian.PutUint32(bitmapEntry[20:24], bitmapCluster)
	binary.LittleEndian.PutUint64(bitmapEntry[24:32], uint64((clusterCount+7)/8))
	put(bitmapEntry)

	upcaseEntry := make([]byte, 32)
	upcaseEntry[0] = 0x82
	binary.LittleEndian.PutUint32(upcaseEntry[4:8], sfTableChecksum(upcase[:512]))
	binary.LittleEndian.PutUint32(upcaseEntry[20:24], upcaseCluster)
	binary.LittleEndian.PutUint64(upcaseEntry[24:32], 512)
	put(upcaseEntry)

	// Zero-length files: they need no data clusters, so the entry count can be
	// scaled without the image growing with it.
	for i := 0; i < fileCount; i++ {
		put(sfBuildEntrySet(sfEntrySet{
			name:       fmt.Sprintf("bench-%05d.bin", i),
			attrs:      0x20,
			noFatChain: true,
		}))
	}
	for i := 0; i < rootClusters; i++ {
		copy(clusterAt(uint32(2+i)), root[i*bnClusterSize:(i+1)*bnClusterSize])
	}

	// --- Allocation bitmap: everything up to the first free cluster ---
	bitmap := clusterAt(bitmapCluster)
	for cluster := uint32(2); cluster < firstFree; cluster++ {
		index := cluster - 2
		bitmap[index/8] |= 1 << (index % 8)
	}

	// --- Deleted entry sets in the unallocated run, for the carving path ---
	for i := 0; i < bnFreeClusters/2; i++ {
		data := clusterAt(firstFree + uint32(i))
		at := 0
		for j := 0; j < 24; j++ {
			records := sfBuildEntrySet(sfEntrySet{
				name:       fmt.Sprintf("erased-%03d-%03d.bin", i, j),
				attrs:      0x20,
				noFatChain: true,
				deleted:    true,
			})
			if at+len(records) > len(data) {
				break
			}
			copy(data[at:], records)
			at += len(records)
		}
	}

	return image
}

func openBenchImage(b *testing.B, fileCount int) *libxfat.ExFAT {
	b.Helper()

	image := buildBenchImage(fileCount)
	path := filepath.Join(b.TempDir(), "bench.exfat")
	if err := os.WriteFile(path, image, 0o600); err != nil {
		b.Fatalf("write image: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		b.Fatalf("open image: %v", err)
	}
	b.Cleanup(func() { _ = file.Close() })

	fs, err := libxfat.Open(libxfat.Source{
		Reader: file, Size: int64(len(image)), Strict: true,
	})
	if err != nil {
		b.Fatalf("open volume: %v", err)
	}
	return fs
}

func BenchmarkReadRootDir(b *testing.B) {
	for _, files := range []int{100, 1000} {
		b.Run(fmt.Sprintf("files=%d", files), func(b *testing.B) {
			fs := openBenchImage(b, files)
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				if _, err := fs.ReadRootDir(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkWalkTree(b *testing.B) {
	fs := openBenchImage(b, 1000)
	root, err := fs.ReadRootDir()
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := fs.AllEntries(root); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkContiguousFilePaths(b *testing.B) {
	fs := openBenchImage(b, 1000)
	root, err := fs.ReadRootDir()
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := fs.ContiguousFilePaths(root, "/"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRecoverDeletedEntries is the path that reads every unallocated
// cluster on the volume, and currently allocates a fresh cluster buffer for
// each one.
func BenchmarkRecoverDeletedEntries(b *testing.B) {
	fs := openBenchImage(b, 100)
	if _, err := fs.ReadRootDir(); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := fs.RecoverDeletedEntries(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerifyNameHash(b *testing.B) {
	fs := openBenchImage(b, 100)
	root, err := fs.ReadRootDir()
	if err != nil {
		b.Fatal(err)
	}
	all, err := fs.AllEntries(root)
	if err != nil {
		b.Fatal(err)
	}
	// Warm the up-case table so the loop measures hashing, not loading.
	_ = fs.VerifyNameHash(all[0])

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		for _, entry := range all {
			_ = fs.VerifyNameHash(entry)
		}
	}
}

// openSuperfloppyBench opens the audited fixture rather than buildBenchImage,
// because buildBenchImage's files are all zero-length and contiguous - it has no
// FAT chain for ClusterList to walk.
func openSuperfloppyBench(b *testing.B) *libxfat.ExFAT {
	b.Helper()

	image := buildSuperfloppyImage()
	path := filepath.Join(b.TempDir(), "superfloppy.exfat")
	if err := os.WriteFile(path, image, 0o600); err != nil {
		b.Fatalf("write fixture: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		b.Fatalf("open fixture: %v", err)
	}
	b.Cleanup(func() { _ = file.Close() })

	fs, err := libxfat.Open(libxfat.Source{
		Reader: file, Size: int64(len(image)),
		Strict: true, IgnorePartitionOffset: true,
	})
	if err != nil {
		b.Fatalf("open volume: %v", err)
	}
	return fs
}

// BenchmarkClusterListChained measures the FAT-chained branch, which used to
// build the contiguous cluster range and immediately throw it away.
//
// The fixture's fragmented.bin spans two clusters, so the bytes here understate
// what the change is worth: the discarded slice was sized by the file, so on a
// real volume it scales with the size of every fragmented file walked. The
// allocation count is the part that transfers directly.
func BenchmarkClusterListChained(b *testing.B) {
	fs := openSuperfloppyBench(b)
	root, err := fs.ReadRootDir()
	if err != nil {
		b.Fatal(err)
	}

	var target libxfat.Entry
	for _, entry := range root {
		if entry.Name() == "fragmented.bin" {
			target = entry
			break
		}
	}
	if target.Name() != "fragmented.bin" {
		b.Fatal("fragmented.bin not found in fixture root")
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, _, err := fs.ClusterList(target); err != nil {
			b.Fatal(err)
		}
	}
}

// benchFragmentTarget returns the fixture's one genuinely fragmented file.
func benchFragmentTarget(b *testing.B, fs *libxfat.ExFAT) libxfat.Entry {
	b.Helper()

	root, err := fs.ReadRootDir()
	if err != nil {
		b.Fatal(err)
	}
	for _, entry := range root {
		if entry.Name() == "fragmented.bin" {
			return entry
		}
	}
	b.Fatal("fragmented.bin not found in fixture root")
	return libxfat.Entry{}
}

// BenchmarkFragmentOffsetsFragmented is the cost of mapping one fragmented file
// without reading any of it.
//
// It is the operation a whole-volume change-detection pass repeats once per file,
// so its allocation count is the number that matters: the fixture's fragmented.bin
// spans two clusters, which understates the wall-clock saving but not the shape of
// the work. Nothing here scales with file size - only with the number of runs.
func BenchmarkFragmentOffsetsFragmented(b *testing.B) {
	fs := openSuperfloppyBench(b)
	target := benchFragmentTarget(b, fs)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		result, err := fs.FragmentOffsetsWithOptions(target, libxfat.FragmentOptions{})
		if err != nil {
			b.Fatal(err)
		}
		if len(result.Ranges) != 2 {
			b.Fatalf("got %d ranges, want the 2 runs of a fragmented file", len(result.Ranges))
		}
	}
}
