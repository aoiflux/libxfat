package libxfat

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

type VBR struct {
	signature         string
	vbrOffset         uint64
	volumeSize        uint64
	fatOffset         uint32
	fatSize           uint32
	numberOfFats      byte
	dataRegionOffset  uint32
	nbClusters        uint32
	rootDirCluster    uint32
	sn                []byte
	version           uint16
	sectorSize        uint32
	sectorsPerCluster uint32
	clusterSize       uint64
	firstFat          uint64
	percentInUse      byte
	dataAreaStart     uint64
	dimage            io.ReaderAt
	// base is the absolute byte offset within dimage at which the volume (its
	// volume boot record) begins. Every absolute offset the library computes -
	// including the values returned by GetClusterOffset - is relative to the
	// start of dimage, not to the start of the volume.
	base int64
	// size is the length of dimage in bytes, or 0 when it is not known.
	size        int64
	volumeLabel string
	bitmapEntry Entry
	upcaseEntry Entry
	// upcaseTable is the volume's up-case table, decompressed, indexed by code
	// unit. It is loaded on demand because only name hashing needs it. Units at
	// or past its end map to themselves.
	upcaseTable upcaseTable
	// visitPool hands out the per-walk scratch a cluster walk needs, so a tree
	// walk does not allocate a read buffer and a loop-detection set for every
	// directory it descends into.
	//
	// It is a sync.Pool because scratch is now handed out to concurrent walks:
	// the hand-rolled free list it replaces was mutated without synchronisation,
	// and two walks sharing it could be given the same state. It is held by
	// pointer so that a VBR remains copyable - a sync.Pool value would make every
	// VBR assignment a vet error, and sharing one pool between copies of a volume
	// is safe, which is the whole point of the type.
	//
	// A nil pool is usable: scratch is allocated per walk instead. That keeps a
	// zero-value VBR, which the unit tests build directly, working.
	visitPool *sync.Pool
}

// visitState is the scratch a cluster walk needs: the topology scratch every
// walk needs, plus the cluster-sized read buffer only a walk that reads file
// data needs.
//
// The buffer is allocated on first use rather than up front, because the two
// kinds of walk have very different costs. Enumerating a file's extents reads
// only FAT entries, and on a volume with 32 MiB clusters an eagerly allocated
// buffer is 32 MiB held to read no file data at all.
//
// These are pooled rather than held as single shared fields on VBR. Nothing
// nests a walk inside another today - every visitor is internal and none reads
// - but a shared buffer would corrupt the outer walk silently if one ever did,
// and the cost of a free list is the same.
type visitState struct {
	walkScratch
	buf []byte
}

// ensureBuf returns the cluster-sized read buffer, allocating it on first use.
func (s *visitState) ensureBuf(v *VBR) []byte {
	if uint64(len(s.buf)) != v.clusterSize {
		s.buf = make([]byte, v.clusterSize)
	}
	return s.buf
}

// reset readies pooled scratch for a new walk. The FAT window holds a cursor and
// the loop set holds the previous walk's clusters, so neither may be inherited; a
// buffer sized for another volume's geometry is dropped rather than kept.
func (s *visitState) reset(v *VBR) {
	s.fat.invalidate()
	if s.seen != nil {
		clear(s.seen)
	}
	if uint64(len(s.buf)) != v.clusterSize {
		s.buf = nil
	}
}

// Entry is one filesystem object as the library reports it: a real exFAT entry
// set, or one of the synthetic entries the library invents for regions that have
// no directory record ($MBR, $FAT1).
//
// It is copied by value everywhere and a whole-volume walk holds hundreds of
// thousands of them, so the fields are grouped widest-first. That grouping is
// purely a packing concern and carries no meaning; the comments say what each
// field is for. sizeof is pinned by TestEntrySize.
type Entry struct {
	dataLen      uint64
	validDataLen uint64
	// regionOffset is the byte offset of a synthetic region entry; see isRegion.
	regionOffset uint64

	name string
	// rawName is the name exactly as decoded from the name records, before any
	// decoration: no deleted marker, no placeholder, no path prefix. Verifying
	// the name hash needs the name the volume actually recorded, and callers
	// routinely rewrite name into a full path.
	rawName string

	entryCluster   uint32
	modified       uint32
	created        uint32
	accessed       uint32
	secondaryCount uint32
	readNameLen    uint32

	entryAttr uint16
	// expectedSetChecksum and computedSetChecksum are the entry set checksum as
	// recorded on disk and as recomputed from the set's own bytes.
	expectedSetChecksum uint16
	computedSetChecksum uint16
	// nameHash is the hash of the up-cased name recorded in the stream
	// extension entry.
	nameHash uint16

	etype        byte
	modified10ms byte
	created10ms  byte
	// exFAT timestamps are wall-clock readings; these bytes carry the UTC
	// offset that makes them absolute. Bit 7 set means the offset is valid,
	// bits 0..6 are a signed count of 15-minute increments.
	modifiedUtcOffset byte
	createdUtcOffset  byte
	accessedUtcOffset byte
	nameLen           byte
	noFatChain        bool
	// isRegion marks a synthetic entry that maps onto a fixed byte range of the
	// image ($MBR, $FAT1, $FAT2) rather than onto a cluster chain.
	isRegion bool
	// nameChecksumChecked records whether the entry set's checksum was compared
	// against the value recorded on disk. It is false in optimistic mode, where
	// the comparison is skipped, and false for synthetic and virtual entries,
	// which are not entry sets at all.
	nameChecksumChecked bool
	// nameChecksumVerified is meaningful only when nameChecksumChecked is set.
	nameChecksumVerified bool
	// nameSynthetic marks an entry whose name records held nothing usable, so
	// the library supplied a placeholder rather than leaving it unaddressable.
	nameSynthetic bool
}

func (e Entry) IsInvalid() bool {
	return !e.IsValid() || (e.IsSpecialFile() && !e.IsVirtualEntry())
}
func (e Entry) IsValid() bool {
	return e.etype == EXFAT_DIRRECORD_FILEDIR || e.IsVirtualEntry()
}
func (e Entry) IsVirtualEntry() bool {
	// Virtual entries created for filesystem metadata (like $MBR, $FAT1, $OrphanFiles)
	return e.name == MBR || e.name == FAT1 || e.name == FAT2 || e.name == ORPHANFILES
}
func (e Entry) IsBitmapUpcase() bool {
	return e.name == BITMAP || e.name == UPCASE || e.etype == EXFAT_DIRRECORD_BITMAP || e.etype == EXFAT_DIRRECORD_UPCASE
}
func (e Entry) IsSpecialFile() bool {
	// Check if entry is a special/metadata file (bitmap, upcase, volume GUID, TexFAT, ACT, or virtual entries)
	if e.IsBitmapUpcase() || e.IsVirtualEntry() {
		return true
	}
	return e.name == VOLUME_GUID || e.name == TEXFAT || e.name == ACT ||
		e.etype == EXFAT_DIRRECORD_VOLUME_GUID || e.etype == EXFAT_DIRRECORD_TEXFAT || e.etype == EXFAT_DIRRECORD_ACT
}

// IsRegion reports whether the entry maps onto a fixed byte range of the image
// ($MBR, $FAT1, $FAT2) rather than onto a cluster chain.
func (e Entry) IsRegion() bool {
	return e.isRegion
}

// GetRegionOffset returns the absolute byte offset of a region entry within the
// image, and whether the entry is a region at all. Region entries lie outside
// the cluster heap, so GetClusterList cannot describe them; this together with
// GetSize gives their full extent.
func (e Entry) GetRegionOffset() (uint64, bool) {
	return e.regionOffset, e.isRegion
}

// IsMetadataStream reports whether the entry is one of the filesystem's own
// cluster-backed metadata streams ($BitMap, $UpCase) and actually has content.
func (e Entry) IsMetadataStream() bool {
	return e.IsBitmapUpcase() &&
		e.dataLen > 0 &&
		uint64(e.entryCluster) >= FIRST_CLUSTER_NUMBER
}

// GetEntryType returns the raw exFAT directory entry type byte, for example
// 0x85 for an allocated file entry, 0x05 for a deleted one, or 0x81/0x82 for
// the allocation bitmap and up-case table. Synthetic entries report 0xFF.
func (e Entry) GetEntryType() byte {
	return e.etype
}

// GetAttributes returns the raw exFAT file attribute bits. Compare against the
// ENTRY_ATTR_* masks.
func (e Entry) GetAttributes() uint16 {
	return e.entryAttr
}

// GetValidDataSize returns the entry's valid data length in bytes: how much of
// the allocated stream was actually written. Bytes between this and GetSize are
// allocated but never written, and may retain earlier content.
//
// Prefer this to GetValidDataLen, which returns a human-readable string.
func (e Entry) GetValidDataSize() uint64 {
	return e.validDataLen
}

// NameChecksumVerified reports whether the entry set's checksum was compared
// against the value recorded on disk and matched. It is false both for a
// mismatch and for an entry whose checksum was never checked - optimistic mode,
// and synthetic entries such as $MBR - so it answers "is this name provably
// intact", not "is this name suspect". For the latter use NameChecksumMismatch.
func (e Entry) NameChecksumVerified() bool {
	return e.nameChecksumChecked && e.nameChecksumVerified
}

// NameChecksumMismatch reports whether the checksum was checked and disagreed.
// A mismatched entry still carries its parsed name.
func (e Entry) NameChecksumMismatch() bool {
	return e.nameChecksumChecked && !e.nameChecksumVerified
}

// NameChecksumError returns an error wrapping ErrNameChecksumMismatch when the
// entry set failed verification, and nil otherwise - including when no check
// was performed. It lets a caller fold name-integrity handling into the same
// error path as everything else.
func (e Entry) NameChecksumError() error {
	if !e.NameChecksumMismatch() {
		return nil
	}
	return fmt.Errorf("%w: %q records checksum 0x%04x, computed 0x%04x",
		ErrNameChecksumMismatch, e.name, e.expectedSetChecksum, e.computedSetChecksum)
}

// EntrySetChecksums returns the checksum recorded in the entry set's primary
// record, the checksum computed over the set as read, and whether the two were
// compared at all. It exists so a report can quote both values rather than just
// the verdict.
func (e Entry) EntrySetChecksums() (expected, computed uint16, checked bool) {
	return e.expectedSetChecksum, e.computedSetChecksum, e.nameChecksumChecked
}

// HasSyntheticName reports whether GetName returns a placeholder the library
// supplied rather than a name read off the volume. It is set when an entry set's
// name records carry no usable characters at all.
//
// The alternative - leaving the name empty - loses the entry: a directory with
// no name is treated as unreadable, so everything beneath it silently vanishes
// from a walk. Entries are located by cluster, so the placeholder costs nothing
// and keeps the subtree addressable. Anything reporting a name to a person
// should check this, because the name is the library's invention, not evidence.
func (e Entry) HasSyntheticName() bool {
	return e.nameSynthetic
}

// RecordedNameHash returns the name hash stored in the entry's stream extension
// entry, and whether the entry records one at all. Use ExFAT.VerifyNameHash to
// check it, which needs the volume's up-case table.
func (e Entry) RecordedNameHash() (uint16, bool) {
	return e.nameHash, e.etype == EXFAT_DIRRECORD_FILEDIR && !e.IsSpecialFile()
}

// GetRawName returns the name exactly as recorded on the volume, without the
// deleted marker, the placeholder given to a nameless entry, or any path a
// caller has composed onto GetName.
func (e Entry) GetRawName() string {
	return e.rawName
}

func (e Entry) GetNameLength() byte {
	return e.nameLen
}
func (e Entry) GetName() string {
	return e.name
}
func (e Entry) IsFile() bool {
	return !e.IsDir()
}
func (e Entry) IsDir() bool {
	return e.entryAttr&ENTRY_ATTR_DIR_MASK > 0
}
func (e Entry) GetEntryCluster() uint32 {
	return e.entryCluster
}
func (e Entry) GetSize() uint64 {
	return e.dataLen
}
func (e Entry) DoesNotHaveFatChain() bool {
	return e.noFatChain
}
func (e Entry) HasFatChain() bool {
	return !e.noFatChain
}
func (e Entry) IsIndexed() bool {
	return !e.IsDeleted()
}
func (e Entry) IsDeleted() bool {
	return entryTypeNormal(e.etype) == (EXFAT_DIRRECORD_FILEDIR&0x7F) && !entryInUse(e.etype)
}
func (e Entry) HasNoName() bool {
	ename := strings.TrimSpace(e.name)
	ename = strings.TrimSuffix(ename, DELETED)
	ename = strings.ReplaceAll(ename, " ", "")
	return ename == ""
}
func (e Entry) NonParsable() bool {
	// Virtual entries (like $MBR, $FAT1, $OrphanFiles) should not be recursively parsed
	if e.IsVirtualEntry() {
		return true
	}
	return e.IsDeleted() || e.IsFile() || e.IsInvalid() || e.HasNoName()
}

// ExFAT is an open exFAT volume.
//
// It holds no parse state. Directory parsing keeps its state in a dirParser
// created per parse, which is what allows one volume to be read from more than
// one goroutine; see dirParser for why the state was moved off this struct.
type ExFAT struct {
	vbr        VBR
	optimistic bool
	// rejectChecksumMismatch drops an entry set whose checksum does not verify
	// instead of reporting the mismatch on the entry. Off by default: the
	// parsed name is evidence even when the set around it is damaged.
	rejectChecksumMismatch bool

	// metaMu guards the handful of VBR fields that are not known when the volume
	// is opened: the label and the $BitMap and $UpCase streams, which a root
	// directory parse discovers, and the up-case table, which is decompressed on
	// first use. Everything else in vbr is fixed by parseVBR and needs no lock.
	//
	// One mutex covers all of them because one parse discovers them together.
	metaMu sync.RWMutex
}

// volumeMetaKnown reports whether a usable $BitMap entry has been published.
func (e *ExFAT) volumeMetaKnown() bool {
	e.metaMu.RLock()
	defer e.metaMu.RUnlock()
	return e.vbr.bitmapEntry.name != "" &&
		e.vbr.bitmapEntry.entryCluster != 0 &&
		e.vbr.bitmapEntry.dataLen != 0
}

// bitmapStream returns the published $BitMap entry.
func (e *ExFAT) bitmapStream() Entry {
	e.metaMu.RLock()
	defer e.metaMu.RUnlock()
	return e.vbr.bitmapEntry
}

// upcaseStream returns the published $UpCase entry.
func (e *ExFAT) upcaseStream() Entry {
	e.metaMu.RLock()
	defer e.metaMu.RUnlock()
	return e.vbr.upcaseEntry
}

// upcaseTableSnapshot returns the decompressed up-case table, or nil when it has
// not been loaded yet. The slice is never mutated after being stored, so handing
// it out under a read lock is safe.
func (e *ExFAT) upcaseTableSnapshot() upcaseTable {
	e.metaMu.RLock()
	defer e.metaMu.RUnlock()
	return e.vbr.upcaseTable
}

// storeUpcaseTable publishes a decompressed table, keeping whichever was stored
// first so that two concurrent loads agree on one answer.
func (e *ExFAT) storeUpcaseTable(table upcaseTable) upcaseTable {
	e.metaMu.Lock()
	defer e.metaMu.Unlock()
	if len(e.vbr.upcaseTable) > 0 {
		return e.vbr.upcaseTable
	}
	e.vbr.upcaseTable = table
	return table
}

// publish installs what a root-directory parse discovered about the volume.
//
// Only the root parse calls it. These records are root-only by specification, and
// the parse that finds them is the only one entitled to act on them; see
// parseOutputs for what went wrong when any directory could.
func (e *ExFAT) publish(out parseOutputs) {
	e.metaMu.Lock()
	defer e.metaMu.Unlock()

	if out.sawLabel {
		e.vbr.volumeLabel = out.volumeLabel
	}
	if out.sawBitmap {
		e.vbr.bitmapEntry = out.bitmapEntry
	}
	if out.sawUpcase {
		// A table decompressed earlier describes a different $UpCase stream, so
		// it is dropped rather than reused when the entry changes.
		if e.vbr.upcaseEntry.entryCluster != out.upcaseEntry.entryCluster ||
			e.vbr.upcaseEntry.dataLen != out.upcaseEntry.dataLen {
			e.vbr.upcaseTable = nil
		}
		e.vbr.upcaseEntry = out.upcaseEntry
	}
}

func (e *ExFAT) GetVolumeLabel() string {
	e.metaMu.RLock()
	defer e.metaMu.RUnlock()
	return e.vbr.volumeLabel
}
