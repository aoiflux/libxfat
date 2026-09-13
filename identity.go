package libxfat

import "fmt"

// FileID is the closest thing exFAT offers to a stable file identity.
//
// exFAT has no inode number and no reuse counter. What it has is a directory
// record at a position inside a parent directory, so identity has to be built
// out of those two facts: which directory listed the file, and where in that
// directory its entry set begins.
//
// EntrySlotIndex is a logical position - a count of 32-byte slots from the start
// of the directory - rather than a byte offset into the image. That matters
// because the two fail differently. Moving or defragmenting a directory changes
// every byte offset in it while leaving every slot index untouched, so the slot
// index survives the one event most likely to disturb a physical address.
// Entry.EntrySetOffset is offered alongside it for callers that want the physical
// address, and it is the right choice for going and reading the record; it is the
// wrong choice for recognising the same file twice.
//
// # What this cannot do
//
// A FileID identifies a slot, not a file. Nothing on the volume records that a
// slot was reused, so:
//
//   - A file deleted and replaced by another file in the same slot of the same
//     directory has exactly the same FileID as its predecessor. There is no field
//     anywhere on an exFAT volume that would distinguish the two.
//   - Creating and deleting files in a directory shifts nothing, because exFAT
//     does not compact directories, but a directory rebuilt by a repair tool may
//     renumber all of it.
//   - A file moved between directories gets a new FileID, and nothing connects it
//     to the old one.
//
// So matching FileIDs is evidence that two observations concern the same slot,
// which is usually but not always the same file. Rename and move detection built
// on this is inference, and a consumer should present it as inference rather than
// as a finding. That is a limitation of the format, not of this library, and no
// amount of care in the caller removes it.
//
// exFAT is at least better placed than FAT12/16 here: its root directory is an
// ordinary cluster chain with a real first cluster, so entries directly under the
// root carry the same kind of parent reference as entries anywhere else, with no
// sentinel standing in for a fixed-position root.
type FileID struct {
	ParentFirstCluster uint32 `json:"parent_first_cluster"`
	EntrySlotIndex     uint32 `json:"entry_slot_index"`
}

// String renders a FileID as parent:slot, which is short enough to log and
// unambiguous enough to compare.
func (f FileID) String() string {
	return fmt.Sprintf("%d:%d", f.ParentFirstCluster, f.EntrySlotIndex)
}

// FileID returns entry's composite identity, and reports whether one could be
// built at all.
//
// It is false for an entry that has no parent directory to be identified within:
// the synthetic region entries ($MBR, $FAT1, $FAT2), which describe byte ranges
// rather than directory records, and entries carved out of unallocated space by
// RecoverDeletedEntries, whose parent directory is gone. Those entries are still
// locatable - EntrySetOffset says where their records are - they are just not
// identifiable, and returning a FileID of 0:0 for all of them would collide them
// into one.
//
// Read the limits in the FileID documentation before using this for rename or
// move detection.
func (e *ExFAT) FileID(entry Entry) (FileID, bool) {
	if !e.vbr.isValidCluster(entry.parentFirstCluster) {
		return FileID{}, false
	}
	return FileID{
		ParentFirstCluster: entry.parentFirstCluster,
		EntrySlotIndex:     entry.entrySlotIndex,
	}, true
}

// EntrySetOffset is the absolute image offset of the first record of this entry's
// entry set - the 0x85 file directory entry - and reports whether the offset is
// known.
//
// The offset is absolute: it includes Source.Base, so it can be handed straight
// to a reader over the whole image rather than over the volume.
//
// It is false when the entry has no directory record to point at, or when the
// record's location was not established during parsing:
//
//   - the synthetic region entries, which are byte ranges rather than records
//   - entries parsed from a buffer the library was not told the origin of
//
// For a fragmented directory the offset is still correct: it is derived from the
// cluster the chain walk actually reached, not from an assumption that the
// directory is contiguous.
//
// This is a physical address and it moves if the parent directory is relocated.
// Use FileID to recognise an entry across two observations of a volume, and this
// to go and read its records.
func (e Entry) EntrySetOffset() (int64, bool) {
	return e.entrySetOffset, e.entrySetOffset > 0
}

// ParentFirstCluster is the first cluster of the directory this entry was read
// from, or zero if that is unknown - see FileID for when it is.
//
// For an entry directly under the root this is the volume's root directory
// cluster, which is a real cluster on exFAT rather than a sentinel.
func (e Entry) ParentFirstCluster() uint32 {
	return e.parentFirstCluster
}

// EntrySlotIndex is the position of this entry's primary record within its parent
// directory, counted in 32-byte slots from the start of the directory.
//
// It is meaningful only together with ParentFirstCluster, and only when FileID
// reports true; on its own it is a small integer that many entries share.
func (e Entry) EntrySlotIndex() uint32 {
	return e.entrySlotIndex
}
