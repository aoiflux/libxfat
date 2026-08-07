package libxfat

import (
	"fmt"
	"io"
	"strings"
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
	size           int64
	volumeLabel    string
	bitmcapCluster uint32
	bitmapLength   uint64
	upcaseCluster  uint32
	upcaseLength   uint64
	bitmapEntry    Entry
}

type Entry struct {
	etype        byte
	dataLen      uint64
	entryCluster uint32
	modified     uint32
	created      uint32
	accessed     uint32
	modified10ms byte
	created10ms  byte
	// exFAT timestamps are wall-clock readings; these bytes carry the UTC
	// offset that makes them absolute. Bit 7 set means the offset is valid,
	// bits 0..6 are a signed count of 15-minute increments.
	modifiedUtcOffset byte
	createdUtcOffset  byte
	accessedUtcOffset byte
	entryAttr         uint16
	noFatChain        bool
	name              string
	seenRecords       []byte
	secondaryCount    uint32
	nameLen           byte
	readNameLen       uint32
	validDataLen      uint64
	// isRegion marks a synthetic entry that maps onto a fixed byte range of the
	// image ($MBR, $FAT1, $FAT2) rather than onto a cluster chain.
	isRegion     bool
	regionOffset uint64
	// nameChecksumChecked records whether the entry set's checksum was compared
	// against the value recorded on disk. It is false in optimistic mode, where
	// the comparison is skipped, and false for synthetic and virtual entries,
	// which are not entry sets at all.
	nameChecksumChecked bool
	// nameChecksumVerified is meaningful only when nameChecksumChecked is set.
	nameChecksumVerified bool
	expectedSetChecksum  uint16
	computedSetChecksum  uint16
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

type ExFAT struct {
	vbr          VBR
	virtualEntry Entry
	entry        Entry
	offset       int
	remainingSC  int
	entryState   int
	clusterdata  []byte
	dirtype      byte
	optimistic   bool
	// rejectChecksumMismatch drops an entry set whose checksum does not verify
	// instead of reporting the mismatch on the entry. Off by default: the
	// parsed name is evidence even when the set around it is damaged.
	rejectChecksumMismatch bool
	// Parsing state for filename/checksum assembly
	setChecksum      uint16
	expectedChecksum uint16
	expectedSC       int
	expectedNameLen  int
	nameUnits        []uint16
	// setInUse is the allocation state of the primary record that opened the
	// current set. Secondary records must agree with it, otherwise a deleted
	// record is being folded into an allocated set or vice versa.
	setInUse bool
	// sawStream guards against emitting a set that never carried a stream
	// extension - it would have no cluster, no length and no name length - and
	// against a second stream extension decrementing the secondary count twice.
	sawStream bool
}

func (e *ExFAT) initEntryState(clusetrdata []byte, offset, remainingSC, entryState int) {
	e.virtualEntry = Entry{}
	e.entry = Entry{}
	e.clusterdata = clusetrdata
	e.offset = offset
	e.remainingSC = remainingSC
	e.entryState = entryState
}
func (e *ExFAT) GetVolumeLabel() string {
	return e.vbr.volumeLabel
}
