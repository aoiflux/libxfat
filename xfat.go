// Package libxfat is a read-only, forensically-minded exFAT parser.
//
// It never writes to the image, it reads exclusively through io.ReaderAt so it
// can be layered directly over a decoded EWF/VHD device or an io.SectionReader
// scoped to a single partition, and it surfaces what it could not verify rather
// than quietly papering over it.
//
// # Opening a volume
//
// Open is the constructor to reach for; it takes a Source describing where the
// volume lives and how strictly to validate it. New and NewFromReaderAt are the
// older, narrower forms, kept for compatibility.
//
//	fs, err := libxfat.Open(libxfat.Source{
//		Reader: file,
//		Size:   size,
//		Strict: true,
//	})
//
// # Walking the volume
//
// ReadRootDir returns the root directory's entries plus the synthetic entries
// describing the filesystem's own structures ($MBR, $FAT1, $FAT2,
// $OrphanFiles). From there:
//
//   - ReadDir and ReadDirs descend one level.
//   - GetAllEntries and GetIndexableEntries flatten the whole tree.
//   - GetFullPathIndexableEntries does the same with paths composed.
//   - RecoverDeletedEntries carves deleted entry sets out of unallocated
//     clusters.
//
// Content comes out through ExtractEntryContent, or through GetClusterList and
// Entry.GetRegionOffset for callers that would rather do their own reading.
//
// # Strict mode
//
// Strict enables the checks that matter for evidence: the PartitionOffset
// cross-check performed when the volume is opened, and verification of each
// directory entry set's checksum while parsing. A failed checksum never costs
// you the parsed name - see Entry.NameChecksumMismatch - unless you ask for
// that explicitly with Source.RejectChecksumMismatch.
//
// # Concurrency
//
// Several volumes may share one Reader concurrently, provided the Reader itself
// is safe for concurrent ReadAt, as *os.File and bytes.Reader are. A single
// ExFAT value is not safe for concurrent use: directory parsing keeps mutable
// state on it. Open one per goroutine.
package libxfat

import (
	"io"
	"os"
)

// Source describes where an exFAT volume lives and how strictly it should be
// validated. It exists because "the byte offset we read from" and "the
// PartitionOffset the volume claims for itself" are independent facts, and
// conflating them makes it impossible to open a partition-relative reader in
// strict mode.
//
// The zero value is not usable: Reader is required.
type Source struct {
	// Reader is the backing image. It may be a *os.File, a bytes.Reader, an
	// io.SectionReader over a partition, or any decoded-container reader
	// (EWF, VHD, ...) that implements io.ReaderAt.
	//
	// Because reads no longer move a shared seek cursor, several independently
	// opened volumes may share one Reader concurrently, provided the Reader
	// itself is safe for concurrent ReadAt (as *os.File and bytes.Reader are).
	// A single ExFAT value is still not safe for concurrent use: directory
	// parsing keeps mutable state on it. Open one per goroutine.
	Reader io.ReaderAt

	// Size is the length of Reader in bytes. Zero means unknown, which disables
	// bounds checking; supply it when you can, so that a malformed image yields
	// a clean io.ErrUnexpectedEOF instead of whatever the reader decides to do
	// with an out-of-range offset.
	Size int64

	// Base is the absolute byte offset within Reader at which the volume boot
	// record begins. Use 0 when Reader is already scoped to the volume (for
	// example an io.SectionReader over a single partition).
	Base int64

	// Strict enables forensic validation: the PartitionOffset cross-check below
	// and the file-name checksum verification performed while parsing directory
	// entry sets. It is the inverse of the historical optimistic flag and is the
	// recommended setting for evidence processing.
	Strict bool

	// PartitionLBA is the sector address at which this volume is expected to
	// live on its parent disk, as reported by the partition table. It is only
	// consulted when Strict is set, and it is what lets a partition-relative
	// Reader (Base == 0) still be validated against a non-zero PartitionOffset.
	//
	// When left at 0, the expected PartitionOffset is derived from Base instead,
	// which reproduces the pre-v1.1.0 behaviour for whole-disk images.
	PartitionLBA uint64

	// IgnorePartitionOffset skips the PartitionOffset cross-check entirely while
	// keeping the rest of strict mode. Many imaging tools and virtual disk
	// formats write a zero PartitionOffset regardless of where the volume
	// actually sits, so this is a legitimate setting rather than an escape
	// hatch - but it does discard one consistency signal.
	IgnorePartitionOffset bool

	// RejectChecksumMismatch drops any file entry set whose checksum fails
	// verification, rather than returning the entry with the mismatch recorded
	// on it. It is only consulted when Strict is set, since that is the only
	// mode in which the checksum is checked at all.
	//
	// It is off by default, and should stay off for most evidence work: a
	// damaged entry set is usually more interesting than a missing one, and the
	// name parsed out of it is still the only record of what the file was
	// called. Turn it on when downstream code cannot tolerate an entry whose
	// metadata may be wrong. Either way, use Entry.NameChecksumMismatch to find
	// out which entries did not verify.
	RejectChecksumMismatch bool
}

// New opens an exFAT volume from an image file. offset is the volume's start
// expressed in 512-byte sectors.
//
// It is equivalent to Open with Strict set to !optimistic and Base set to
// offset*512, and is retained unchanged for compatibility.
func New(imagefile *os.File, optimistic bool, offset ...uint64) (ExFAT, error) {
	if imagefile == nil {
		return ExFAT{}, ErrNilReader
	}

	// A failed Stat only costs us bounds checking, so it is not fatal: raw
	// device handles routinely report a zero size.
	var size int64
	if info, err := imagefile.Stat(); err == nil && info.Mode().IsRegular() {
		size = info.Size()
	}

	return NewFromReaderAt(imagefile, size, optimistic, offset...)
}

// NewFromReaderAt opens an exFAT volume from any io.ReaderAt, which is what
// allows the library to be layered directly over a decoded EWF/VHD device or an
// io.SectionReader scoped to a partition, with no temporary file in between.
//
// size is the length of r in bytes, or 0 if unknown. offset is the volume's
// start expressed in 512-byte sectors, matching New.
//
// Note that in strict mode (optimistic == false) the volume's recorded
// PartitionOffset is required to agree with offset. If r is already scoped to
// the partition then offset is 0 while the volume still records its true LBA,
// and the two will not agree; use Open with PartitionLBA or
// IgnorePartitionOffset for that case.
func NewFromReaderAt(r io.ReaderAt, size int64, optimistic bool, offset ...uint64) (ExFAT, error) {
	if len(offset) < 1 {
		offset = append(offset, 0)
	}

	base, err := safeInt64(offset[0] * SECTOR_SIZE)
	if err != nil {
		return ExFAT{}, err
	}

	return open(Source{
		Reader: r,
		Size:   size,
		Base:   base,
		Strict: !optimistic,
	})
}

// Open opens an exFAT volume described by src. It is the constructor to reach
// for when the volume is not simply "a whole-disk image at a 512-byte sector
// boundary" - in particular when layering over a partition reader.
func Open(src Source) (*ExFAT, error) {
	fs, err := open(src)
	if err != nil {
		return nil, err
	}
	return &fs, nil
}

// open is the single real constructor. It returns the partially populated
// value alongside any error, matching what New has always done.
func open(src Source) (ExFAT, error) {
	var exfatdata ExFAT

	if src.Reader == nil {
		return exfatdata, ErrNilReader
	}
	if src.Base < 0 {
		return exfatdata, ErrOutOfBounds
	}

	// The rest of the parser still speaks in terms of "optimistic".
	exfatdata.optimistic = !src.Strict
	exfatdata.rejectChecksumMismatch = src.Strict && src.RejectChecksumMismatch

	var err error
	exfatdata.vbr, err = parseVBR(src)
	return exfatdata, err
}
