package libxfat

import (
	"fmt"
	"io"
)

// File is an open content stream over an entry, read straight out of the image.
//
// It is what makes the library usable in a pipeline: comparing or hashing a file's
// content no longer means extracting it to the host filesystem first. Nothing here
// writes anything anywhere.
//
// A File is not safe for concurrent use - it caches its resolved ranges and holds
// a read cursor. Open the entry again for a second goroutine, or use ReaderAt,
// which holds no cursor.
type File struct {
	fs    *ExFAT
	entry Entry
	// size is the length the directory entry records, which is not necessarily
	// the number of bytes that can be located. Reads are bounded by the latter.
	size      int64
	validSize int64
	readOff   int64
	// reader is built on first read and caches the resolved ranges, so repeated
	// reads do not re-walk the FAT.
	reader    *extentReader
	truncated bool
	opts      FragmentOptions
}

// OpenEntry opens entry's content stream for reading.
//
// Alongside regular files it accepts the filesystem's own metadata - the $BitMap
// and $UpCase streams and the $MBR, $FAT1 and $FAT2 regions - matching what
// ExtractEntryContent has always accepted. It refuses a deleted entry, whose
// chain has been freed; locate one with FragmentOffsetsWithOptions and read the
// ranges directly, so that the guesswork stays visible at the call site.
//
// An entry with no allocation - $OrphanFiles, an empty file, a directory with a
// zero length - opens as an empty stream rather than as an error. There is nothing
// to read, which reads return as io.EOF, and that is a different statement from
// the entry being unreadable.
func (e *ExFAT) OpenEntry(entry Entry) (*File, error) {
	if entry.IsDeleted() {
		return nil, ErrDeletedEntry
	}
	if !entry.IsRegion() && !entry.IsMetadataStream() && entry.IsInvalid() {
		return nil, ErrInvalidEntry
	}

	size, err := safeInt64(entry.dataLen)
	if err != nil {
		return nil, err
	}
	validSize, err := safeInt64(entry.validDataLen)
	if err != nil {
		return nil, err
	}

	return &File{fs: e, entry: entry, size: size, validSize: validSize}, nil
}

// ReadEntry reads the whole of entry's content into memory.
//
// It is the one-line form of OpenEntry followed by ReadAll, for a caller that
// wants the bytes and nothing else.
func (e *ExFAT) ReadEntry(entry Entry) ([]byte, error) {
	f, err := e.OpenEntry(entry)
	if err != nil {
		return nil, err
	}
	return f.ReadAll()
}

// Name returns the entry's name as the library reports it.
func (f *File) Name() string { return f.entry.name }

// Size is the length recorded in the directory entry. It is what the volume
// claims, and on a damaged image it may be more than can actually be located;
// compare against Located.
func (f *File) Size() int64 { return f.size }

// ValidSize is the entry's ValidDataLength: how much of the allocation was ever
// written. Bytes between it and Size are readable but were never written by this
// file.
func (f *File) ValidSize() int64 { return f.validSize }

// Entry returns the entry this File was opened from.
func (f *File) Entry() Entry { return f.entry }

// IsDir reports whether the entry is a directory.
func (f *File) IsDir() bool { return f.entry.IsDir() }

// SetFragmentOptions changes how the content's runs are derived. It must be called
// before the first read, which is when the ranges are resolved and cached.
func (f *File) SetFragmentOptions(opts FragmentOptions) {
	f.opts = opts
	f.reader = nil
}

// extents resolves and caches the entry's runs.
func (f *File) extents() (*extentReader, error) {
	if f.reader != nil {
		return f.reader, nil
	}

	result, err := f.fs.FragmentOffsetsWithOptions(f.entry, f.opts)
	if err != nil {
		return nil, err
	}
	f.truncated = result.Truncated
	f.reader = newExtentReader(&f.fs.vbr, result.Ranges)
	return f.reader, nil
}

// Located returns the number of bytes that could actually be located for this
// file, which is less than Size when the chain is short.
func (f *File) Located() (int64, error) {
	r, err := f.extents()
	if err != nil {
		return 0, err
	}
	return r.Size(), nil
}

// Fragments returns the entry's coalesced runs.
func (f *File) Fragments() ([]Range, error) {
	return f.fs.FragmentOffsets(f.entry)
}

// FragmentsWithOptions returns the entry's runs along with how they were derived.
func (f *File) FragmentsWithOptions(opts FragmentOptions) (*FragmentResult, error) {
	return f.fs.FragmentOffsetsWithOptions(f.entry, opts)
}

// Slack returns the unused tail of the file's last cluster.
func (f *File) Slack() (Range, bool, error) { return f.fs.SlackRange(f.entry) }

// Unwritten returns the parts of the allocation past ValidDataLength.
func (f *File) Unwritten() ([]Range, error) { return f.fs.UnwrittenRanges(f.entry) }

// ReadAt reads len(p) bytes at off within the file's content.
//
// The bound that matters here is the number of bytes actually located, not the
// size the directory entry records. The runs may cover less than the entry claims
// when a chain is short, and reading up to the claim would hand back whatever
// happens to follow on disk as though it were this file's content. So a read that
// starts inside the recorded size but past what was located fails, naming both
// numbers, rather than inventing data.
func (f *File) ReadAt(p []byte, off int64) (int, error) {
	if f.entry.IsDir() && f.size == 0 {
		return 0, ErrNoContent
	}
	if off < 0 {
		return 0, fmt.Errorf("%w: negative offset %d", ErrOutOfBounds, off)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= f.size {
		return 0, io.EOF
	}

	r, err := f.extents()
	if err != nil {
		return 0, err
	}
	if off >= r.Size() {
		return 0, fmt.Errorf("%w: %d of %d bytes recoverable for %q",
			ErrTruncatedChain, r.Size(), f.size, f.entry.name)
	}

	// The recorded size only ever clamps the request down.
	if limit := f.size - off; int64(len(p)) > limit {
		p = p[:limit]
	}
	return r.ReadAt(p, off)
}

// Read reads from the file's current position.
func (f *File) Read(p []byte) (int, error) {
	n, err := f.ReadAt(p, f.readOff)
	f.readOff += int64(n)
	return n, err
}

// Seek moves the read cursor.
func (f *File) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = f.readOff + offset
	case io.SeekEnd:
		abs = f.size + offset
	default:
		return 0, fmt.Errorf("%w: invalid whence %d", ErrOutOfBounds, whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("%w: seek to %d", ErrOutOfBounds, abs)
	}
	f.readOff = abs
	return abs, nil
}

// ReadAll reads the whole content stream into memory.
//
// It allocates for what can be located rather than for what the entry claims, so
// a hostile size field cannot turn one call into a huge allocation. When less was
// located than claimed, the bytes are returned together with an error wrapping
// ErrTruncatedChain, so that a partially recoverable file is never mistaken for a
// small intact one.
func (f *File) ReadAll() ([]byte, error) {
	r, err := f.extents()
	if err != nil {
		return nil, err
	}
	if r.Size() == 0 {
		return nil, nil
	}

	buf := make([]byte, r.Size())
	n, err := r.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		return buf[:n], err
	}
	buf = buf[:n]

	if int64(n) < f.size {
		return buf, fmt.Errorf("%w: %d of %d bytes recoverable for %q",
			ErrTruncatedChain, n, f.size, f.entry.name)
	}
	return buf, nil
}

// WriteTo streams the content to w, run by run.
func (f *File) WriteTo(w io.Writer) (int64, error) {
	r, err := f.extents()
	if err != nil {
		return 0, err
	}
	return r.clone().WriteTo(w)
}

// Reader returns an io.ReadSeeker over the content.
//
// It holds its own cursor, independent of the File's, and is not safe for
// concurrent use. For that, use ReaderAt.
func (f *File) Reader() (io.ReadSeeker, error) {
	r, err := f.extents()
	if err != nil {
		return nil, err
	}
	return r.clone(), nil
}

// ReaderAt returns an io.ReaderAt over the content.
//
// Unlike Reader it holds no cursor, so it is safe to use from several goroutines
// provided the image's own ReadAt is, which io.ReaderAt implementations are
// required to be. This is the form to hand to code that reads a file in parallel.
func (f *File) ReaderAt() (io.ReaderAt, error) {
	r, err := f.extents()
	if err != nil {
		return nil, err
	}
	return r.clone(), nil
}

// SectionReader returns an io.SectionReader over the content, for a file that
// occupies a single contiguous run.
//
// A fragmented file has no single section to return, and stitching one silently
// would hide the fragmentation from a caller who asked for exactly this; it
// returns ErrFragmented instead. Use ReaderAt for the general case.
func (f *File) SectionReader() (*io.SectionReader, error) {
	r, err := f.extents()
	if err != nil {
		return nil, err
	}
	if len(r.ranges) != 1 {
		return nil, fmt.Errorf("%w: %q occupies %d runs", ErrFragmented, f.entry.name, len(r.ranges))
	}
	return f.fs.vbr.sectionReader(r.ranges[0].StartByte, r.ranges[0].Length)
}
