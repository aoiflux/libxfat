package test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoiflux/libxfat"
)

// testImageBytes builds the same minimal volume as createTestImage, but as a
// plain byte slice so it can be wrapped in any io.ReaderAt.
func testImageBytes() []byte {
	data := make([]byte, testVolumeSectors*testSectorSize)
	writeTestVBR(data)
	writeTestFAT(data[testFatOffsetSector*testSectorSize:])
	writeTestRootDir(data[testDataOffset*testSectorSize:])
	writeTestBitmapData(data)
	return data
}

// summarise reduces entries to a comparable form so two open paths can be
// checked for equivalence without depending on unexported fields.
func summarise(entries []libxfat.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, fmt.Sprintf("%s|0x%02X|%d|%d|%s|%s",
			e.Name(), e.EntryType(), e.Size(), e.FirstCluster(),
			e.ModifiedTime(), e.CreatedTime()))
	}
	return out
}

// probe exercises the read paths that matter to a consumer and reduces every
// result to a canonical line, so two ways of opening the same bytes can be
// compared exhaustively rather than field by field.
func probe(t *testing.T, fs *libxfat.ExFAT) []string {
	t.Helper()

	var out []string
	record := func(format string, args ...any) {
		out = append(out, fmt.Sprintf(format, args...))
	}

	record("clusterSize=%d", fs.ClusterSize())
	record("percentInUse=%d", fs.PercentInUse())
	label, labelErr := fs.VolumeLabel()
	record("volumeLabel=%q err=%v", label, labelErr)
	for cluster := uint32(2); cluster <= 5; cluster++ {
		offset, offErr := fs.ClusterOffset(cluster)
		record("clusterOffset[%d]=%d err=%v", cluster, offset, offErr)
	}

	allocated, err := fs.AllocatedClusters()
	record("allocatedClusters=%d err=%v", allocated, err)
	free, err := fs.FreeClusters()
	record("freeClusters=%d err=%v", free, err)

	root, err := fs.ReadRootDir()
	record("readRootDir err=%v", err)
	for i, line := range summarise(root) {
		record("root[%d]=%s", i, line)
	}

	all, err := fs.AllEntries(root)
	record("getAllEntries err=%v", err)
	for i, line := range summarise(all) {
		record("all[%d]=%s", i, line)
	}

	deleted, err := fs.RecoverDeletedEntries()
	record("recoverDeleted err=%v", err)
	for i, line := range summarise(deleted) {
		record("deleted[%d]=%s", i, line)
	}

	// Fragment mapping and extracted bytes are the two things a forensic
	// consumer depends on most, so compare both directly.
	dir := t.TempDir()
	for _, entry := range root {
		clusters, tail, err := fs.ClusterList(entry)
		record("clusterList[%s]=%v tail=%d err=%v", entry.Name(), clusters, tail, err)

		if !entry.IsMetadataStream() && !entry.IsRegion() {
			continue
		}
		dst := filepath.Join(dir, strings.ReplaceAll(entry.Name(), "$", "_"))
		if err := fs.ExtractEntryContent(entry, dst); err != nil {
			record("extract[%s] err=%v", entry.Name(), err)
			continue
		}
		content, err := os.ReadFile(dst)
		if err != nil {
			record("extract[%s] read err=%v", entry.Name(), err)
			continue
		}
		record("extract[%s]=%x", entry.Name(), sha256.Sum256(content))
	}

	return out
}

// TestReaderAtMatchesFile is the core regression guard for the io.ReaderAt
// conversion: both constructors must produce identical results from identical
// bytes, across every read path a consumer touches.
func TestReaderAtMatchesFile(t *testing.T) {
	image := createTestImage(t)
	fromFile, err := libxfat.New(image, false)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	data := testImageBytes()
	fromReader, err := libxfat.NewFromReaderAt(bytes.NewReader(data), int64(len(data)), false)
	if err != nil {
		t.Fatalf("NewFromReaderAt() error = %v", err)
	}

	fileProbe := probe(t, fromFile)
	readerProbe := probe(t, fromReader)

	if len(fileProbe) == 0 {
		t.Fatal("probe produced no observations")
	}
	if len(fileProbe) != len(readerProbe) {
		t.Fatalf("observation count mismatch: file = %d, reader = %d", len(fileProbe), len(readerProbe))
	}
	for i := range fileProbe {
		if fileProbe[i] != readerProbe[i] {
			t.Fatalf("observation %d mismatch:\n  file   = %s\n  reader = %s", i, fileProbe[i], readerProbe[i])
		}
	}
}

// TestProbeDetectsDifference is a negative control. The equivalence tests above
// are only meaningful if probe can actually report a difference, so feed it two
// images that differ by a single byte of bitmap content and require a mismatch.
func TestProbeDetectsDifference(t *testing.T) {
	original := testImageBytes()
	modified := testImageBytes()

	bitmapOffset := (testDataOffset + (testBitmapCluster - testRootCluster)) * testSectorSize
	modified[bitmapOffset] ^= 0xFF

	openImage := func(data []byte) *libxfat.ExFAT {
		fs, err := libxfat.NewFromReaderAt(bytes.NewReader(data), int64(len(data)), false)
		if err != nil {
			t.Fatalf("NewFromReaderAt() error = %v", err)
		}
		return fs
	}

	a := probe(t, openImage(original))
	b := probe(t, openImage(modified))

	if len(a) < 20 {
		t.Fatalf("probe produced only %d observations, too few to be meaningful", len(a))
	}

	same := len(a) == len(b)
	if same {
		for i := range a {
			if a[i] != b[i] {
				same = false
				break
			}
		}
	}
	if same {
		t.Fatal("probe reported two different images as identical")
	}
}

// TestOpenMatchesNewFromReaderAt keeps the two reader-based constructors from
// drifting apart for the case they both cover.
func TestOpenMatchesNewFromReaderAt(t *testing.T) {
	data := testImageBytes()

	viaNew, err := libxfat.NewFromReaderAt(bytes.NewReader(data), int64(len(data)), false)
	if err != nil {
		t.Fatalf("NewFromReaderAt() error = %v", err)
	}
	viaOpen, err := libxfat.Open(libxfat.Source{
		Reader: bytes.NewReader(data),
		Size:   int64(len(data)),
		Strict: true,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	newProbe := probe(t, viaNew)
	openProbe := probe(t, viaOpen)

	if len(newProbe) != len(openProbe) {
		t.Fatalf("observation count mismatch: %d vs %d", len(newProbe), len(openProbe))
	}
	for i := range newProbe {
		if newProbe[i] != openProbe[i] {
			t.Fatalf("observation %d mismatch:\n  NewFromReaderAt = %s\n  Open            = %s",
				i, newProbe[i], openProbe[i])
		}
	}
}

// TestNewFromReaderAtRejectsNilReader covers the typed-nil trap: once the field
// became an interface, a nil reader would otherwise sail past the nil check.
func TestNewFromReaderAtRejectsNilReader(t *testing.T) {
	if _, err := libxfat.NewFromReaderAt(nil, 0, false); !errors.Is(err, libxfat.ErrNilReader) {
		t.Fatalf("NewFromReaderAt(nil) error = %v, want ErrNilReader", err)
	}
	if _, err := libxfat.New(nil, false); !errors.Is(err, libxfat.ErrNilReader) {
		t.Fatalf("New(nil) error = %v, want ErrNilReader", err)
	}
	if _, err := libxfat.Open(libxfat.Source{}); !errors.Is(err, libxfat.ErrNilReader) {
		t.Fatalf("Open(zero Source) error = %v, want ErrNilReader", err)
	}
}

// TestStrictModeAcceptsWholeDiskOffset confirms the historical behaviour is
// preserved: a volume embedded in a larger image at the LBA it claims.
func TestStrictModeAcceptsWholeDiskOffset(t *testing.T) {
	const partitionLBA = 2048

	volume := testImageBytes()
	binary.LittleEndian.PutUint64(volume[0x40:0x48], partitionLBA)

	disk := make([]byte, partitionLBA*testSectorSize+len(volume))
	copy(disk[partitionLBA*testSectorSize:], volume)

	fs, err := libxfat.NewFromReaderAt(bytes.NewReader(disk), int64(len(disk)), false, partitionLBA)
	if err != nil {
		t.Fatalf("NewFromReaderAt() at LBA %d error = %v", partitionLBA, err)
	}
	if _, err := fs.ReadRootDir(); err != nil {
		t.Fatalf("ReadRootDir() error = %v", err)
	}
}

// TestStrictModeRejectsWrongOffset keeps the cross-check meaningful.
func TestStrictModeRejectsWrongOffset(t *testing.T) {
	volume := testImageBytes()
	binary.LittleEndian.PutUint64(volume[0x40:0x48], 2048)

	_, err := libxfat.NewFromReaderAt(bytes.NewReader(volume), int64(len(volume)), false)
	if !errors.Is(err, libxfat.ErrPartitionOffsetMismatch) {
		t.Fatalf("NewFromReaderAt() error = %v, want ErrPartitionOffsetMismatch", err)
	}
}

// TestPartitionSectionReaderStrict is the case that could not be expressed
// before: a reader already scoped to the partition, opened in strict mode,
// where the volume still records its true LBA.
func TestPartitionSectionReaderStrict(t *testing.T) {
	const partitionLBA = 2048

	volume := testImageBytes()
	binary.LittleEndian.PutUint64(volume[0x40:0x48], partitionLBA)

	disk := make([]byte, partitionLBA*testSectorSize+len(volume))
	copy(disk[partitionLBA*testSectorSize:], volume)

	partition := io.NewSectionReader(bytes.NewReader(disk), partitionLBA*testSectorSize, int64(len(volume)))

	// Without help, strict mode must refuse: base 0 disagrees with LBA 2048.
	if _, err := libxfat.Open(libxfat.Source{
		Reader: partition,
		Size:   int64(len(volume)),
		Strict: true,
	}); !errors.Is(err, libxfat.ErrPartitionOffsetMismatch) {
		t.Fatalf("Open() without PartitionLBA error = %v, want ErrPartitionOffsetMismatch", err)
	}

	// Told where the partition lives, strict mode validates and accepts.
	fs, err := libxfat.Open(libxfat.Source{
		Reader:       partition,
		Size:         int64(len(volume)),
		Strict:       true,
		PartitionLBA: partitionLBA,
	})
	if err != nil {
		t.Fatalf("Open() with PartitionLBA error = %v", err)
	}
	if _, err := fs.ReadRootDir(); err != nil {
		t.Fatalf("ReadRootDir() error = %v", err)
	}

	// A wrong LBA must still be caught.
	if _, err := libxfat.Open(libxfat.Source{
		Reader:       partition,
		Size:         int64(len(volume)),
		Strict:       true,
		PartitionLBA: 4096,
	}); !errors.Is(err, libxfat.ErrPartitionOffsetMismatch) {
		t.Fatalf("Open() with wrong PartitionLBA error = %v, want ErrPartitionOffsetMismatch", err)
	}

	// And the escape hatch keeps the rest of strict mode.
	if _, err := libxfat.Open(libxfat.Source{
		Reader:                partition,
		Size:                  int64(len(volume)),
		Strict:                true,
		IgnorePartitionOffset: true,
	}); err != nil {
		t.Fatalf("Open() with IgnorePartitionOffset error = %v", err)
	}
}

// TestTruncatedImageDoesNotPanic backs the panic-free part of the contract.
func TestTruncatedImageDoesNotPanic(t *testing.T) {
	full := testImageBytes()

	for _, size := range []int{0, 1, 511, 512, 4096, len(full) - 1} {
		truncated := full[:size]
		fs, err := libxfat.NewFromReaderAt(bytes.NewReader(truncated), int64(len(truncated)), true)
		if err != nil {
			continue // refusing to open a truncated image is a fine outcome
		}
		// Whatever happens here, it must be an error and not a panic.
		if _, err := fs.ReadRootDir(); err != nil {
			continue
		}
	}
}

// TestSizeBoundsReads checks that a declared size turns an over-long read into
// a clean error rather than deferring to the reader's own behaviour.
func TestSizeBoundsReads(t *testing.T) {
	data := testImageBytes()

	// Claim the image is shorter than it is; the data region then falls outside.
	fs, err := libxfat.NewFromReaderAt(bytes.NewReader(data), int64(testDataOffset*testSectorSize), true)
	if err != nil {
		t.Skipf("volume did not open with a truncated size: %v", err)
	}

	_, err = fs.ReadRootDir()
	if err == nil {
		t.Fatal("ReadRootDir() succeeded past the declared image size, want an error")
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, libxfat.ErrOutOfBounds) {
		t.Fatalf("ReadRootDir() error = %v, want an EOF or bounds error", err)
	}
}
