package libxfat

import (
	"errors"
)

// ExFAT Constants
const (
	VBR_SIZE                           = 12
	SECTOR_SIZE                 uint64 = 512
	SYNC_OFFSET                        = 0x1fe
	SYNC_VALUE                         = 0x55aa
	EXFAT_SIGN_OFFSET                  = 3
	EXFAT_VBR1_OFFSET                  = 0x40
	EXFAT_VOLSIZE_OFFSET               = 0x48
	EXFAT_FAT1_OFFSET                  = 0x50
	EXFAT_FATSIZE_OFFSET               = 0x54
	EXFAT_DATA_OFFSET                  = 0x58
	EXFAT_NB_CLUSTERS                  = 0x5C
	EXFAT_ROOT_CLUSTER_OFFSET          = 0x60
	EXFAT_SN_OFFSET                    = 0x64
	EXFAT_VERSION_OFFSET               = 0x68
	EXFAT_VOLUME_FLAGS_OFFSET          = 0x6a
	EXFAT_SECTOR_SIZE_OFFSET           = 0x6c
	EXFAT_CLUSTER_SIZE_OFFSET          = 0x6d
	EXFAT_NUMBER_OF_FATS_OFFSET        = 0x6e
	EXFAT_SIGNATURE                    = "EXFAT   "
	EXFAT_PERCENT_USE_OFFSET           = 0x70

	// There is no cluster 0 or cluster 1 in ExFAT. It starts with cluster 2
	FIRST_CLUSTER_NUMBER uint64 = 2
)

// Bits of the VolumeFlags field at EXFAT_VOLUME_FLAGS_OFFSET. Section 3.1.13 of
// the exFAT specification.
const (
	// VOLUME_FLAG_ACTIVE_FAT selects the second FAT on a TexFAT volume. It is
	// meaningless, and must be zero, when NumberOfFats is 1.
	VOLUME_FLAG_ACTIVE_FAT = 0x0001
	// VOLUME_FLAG_VOLUME_DIRTY is set while the volume is mounted for writing and
	// cleared on a clean unmount, so finding it set says the volume was not
	// cleanly unmounted and its metadata may be mid-update.
	VOLUME_FLAG_VOLUME_DIRTY = 0x0002
	// VOLUME_FLAG_MEDIA_FAILURE records that the implementation met read or write
	// failures it could not recover from.
	VOLUME_FLAG_MEDIA_FAILURE = 0x0004
	// VOLUME_FLAG_CLEAR_TO_ZERO carries no meaning an implementation may rely on;
	// it is exposed only because reporting the raw field without it invites a
	// reader to assume the bit is reserved and zero.
	VOLUME_FLAG_CLEAR_TO_ZERO = 0x0008
)

// exfat entries constants
const (
	EXFAT_DIRRECORD_SIZE = 32

	EXFAT_DIRRECORD_BITMAP           = 0x81
	EXFAT_DIRRECORD_UPCASE           = 0x82
	EXFAT_DIRRECORD_LABEL            = 0x83
	EXFAT_DIRRECORD_NOLABEL          = 0x03
	EXFAT_DIRRECORD_FILEDIR          = 0x85
	EXFAT_DIRRECORD_DEL_FILEDIR      = 0x05
	EXFAT_DIRRECORD_VOLUME_GUID      = 0xA0
	EXFAT_DIRRECORD_TEXFAT           = 0xA1
	EXFAT_DIRRECORD_ACT              = 0xE2
	EXFAT_DIRRECORD_STREAM_EXT       = 0xC0
	EXFAT_DIRRECORD_DEL_STREAM_EXT   = 0x40
	EXFAT_DIRRECORD_FILENAME_EXT     = 0xC1
	EXFAT_DIRRECORD_DEL_FILENAME_EXT = 0x41

	// Bits of GeneralSecondaryFlags, the byte at offset 1 of a stream extension
	// entry. Section 7.4.1 of the specification.
	//
	// ALLOCATION_POSSIBLE_FLAG says that FirstCluster and DataLength mean
	// something. With it clear the specification defines both as undefined, so a
	// record naming a first cluster while leaving this bit clear contradicts
	// itself; see Entry.AllocationPossible and
	// FragmentResult.AllocationContradiction. Every formatter sets it on a
	// stream that has an allocation, which is why the contradiction is a finding
	// rather than a routine case.
	//
	// NOT_FAT_CHAIN_FLAG says the stream occupies consecutive clusters and its
	// FAT entries are undefined. Real formatters therefore write 0x01 for a
	// chained stream and 0x03 for a contiguous one.
	ALLOCATION_POSSIBLE_FLAG = 0x01
	NOT_FAT_CHAIN_FLAG       = 0x02
)

const (
	EXFAT_CLUSTER_MASK = 0x0fffffff
	EXFAT_BAD_CLUSTER  = 0x0ffffff7
	EXFAT_EOF_START    = 0x0ffffff8
	EXFAT_EOF_END      = 0x0fffffff
	FINAL_CLUSTER      = EXFAT_EOF_END
)

// entry constants
const (
	ENTRY_ATTR_ATTR_MASK   uint16 = 0x20
	ENTRY_ATTR_DIR_MASK    uint16 = 0x10
	ENTRY_ATTR_SYSTEM_MASK uint16 = 0x04
	ENTRY_ATTR_HIDDEN_MASK uint16 = 0x02
	ENTRY_ATTR_RO_MASK     uint16 = 0x01
)

// entry state constants
const (
	ENTRY_STATE_START        = 0
	ENTRY_STATE_85_SEEN      = 1
	ENTRY_STATE_LAST_C1_SEEN = 2
)

const (
	BITMAP      = "$BitMap"
	UPCASE      = "$UpCase"
	VOLUME      = "$Volume"
	VOLUME_GUID = "$Volume GUID"
	TEXFAT      = "$TexFAT"
	ACT         = "$ACT"
	MBR         = "$MBR"
	FAT1        = "$FAT1"
	FAT2        = "$FAT2"
	ORPHANFILES = "$OrphanFiles"
	EOF         = "EOF"
	// UNNAMED prefixes the placeholder given to an entry whose name records
	// carried no usable characters. See Entry.HasSyntheticName.
	UNNAMED = "$Unnamed"
)

const ZERO_ENTRY_CLUSTER = 0x0

// Sentinel error for EOF - used when reading content may legitimately hit EOF
// Use errors.Is(err, ErrEOF) or errors.Is(err, io.EOF) to check
var ErrEOF = errors.New("EOF")
var ErrDeletedEntry = errors.New("unable to read deleted entry")
var ErrInvalidEntry = errors.New("unable to read invalid entry")
var ErrInvalidCluster = errors.New("invalid cluster")
var ErrBadCluster = errors.New("bad cluster in FAT chain")
var ErrClusterChainLoop = errors.New("cluster chain loop detected")
var ErrAllocationBitmapNotFound = errors.New("allocation bitmap not found")

// ErrNilReader is returned when a volume is opened without a backing reader.
var ErrNilReader = errors.New("nil image reader")

// ErrOutOfBounds is returned when a computed read falls outside the image.
var ErrOutOfBounds = errors.New("read outside image bounds")

// ErrPartitionOffsetMismatch is returned in strict mode when the PartitionOffset
// recorded in the volume boot record disagrees with where the volume was opened.
// The message is kept prefix-compatible with releases before v1.1.0.
var ErrPartitionOffsetMismatch = errors.New("invalid vbr address")

// ErrExfatSignature is returned when the volume boot record is not exFAT.
var ErrExfatSignature = errors.New("exfat signature mismatch")

// ErrSyncValue is returned when the volume boot record lacks the 0x55AA marker.
var ErrSyncValue = errors.New("no sync value in vbr")

// ErrNoContent is returned when an entry has no recoverable content stream.
var ErrNoContent = errors.New("entry has no recoverable content")

// ErrNameChecksumMismatch reports that a file entry set's recorded
// EntrySetChecksum disagrees with the checksum computed over the set as read.
// The entry's name is still returned: the mismatch is a statement about the
// integrity of the set, not a reason to discard the only copy of the name.
//
// Reach it through Entry.NameChecksumError, and test with errors.Is.
var ErrNameChecksumMismatch = errors.New("file name entry set checksum mismatch")

// ErrNameHashMismatch reports that an entry's name does not hash to the value
// recorded alongside it in the stream extension entry. It is a statement about
// the name specifically, and independent of the entry set checksum: a set can
// satisfy one and fail the other.
//
// Reach it through ExFAT.VerifyNameHash, and test with errors.Is.
var ErrNameHashMismatch = errors.New("file name hash mismatch")

// ErrNoNameHash is returned by VerifyNameHash for entries that are not file
// entry sets and so record no name hash: the synthetic $MBR, $FAT1 and $FAT2,
// and the $BitMap and $UpCase streams.
var ErrNoNameHash = errors.New("entry records no name hash")

// ErrUpcaseTableNotFound is returned when the volume's up-case table cannot be
// located or read. Name hashing is defined in terms of that table, so without
// it the hash cannot be checked at all.
var ErrUpcaseTableNotFound = errors.New("up-case table not found")

// ErrNoClusterMapping is returned by ClusterList for entries that are backed
// by a fixed byte range rather than by clusters, namely $MBR, $FAT1 and $FAT2.
//
// It is a statement about the cluster API specifically. FragmentOffsets accepts
// those entries and describes them as the byte ranges they are, which is what a
// caller comparing a file against image byte ranges actually needs.
var ErrNoClusterMapping = errors.New("entry is not mapped through clusters")

// ErrTruncatedChain reports that fewer bytes could be located for an entry than
// its directory record claims. The ranges that were located are returned
// alongside it: a file found as far as it can be followed is evidence, and
// discarding it in favour of an error would lose that.
//
// It is the expected outcome for a deleted entry, whose chain has been freed.
var ErrTruncatedChain = errors.New("located fewer bytes than the entry records")

// ErrFragmented is returned when an operation needs a single contiguous run and
// the entry does not have one.
var ErrFragmented = errors.New("entry is fragmented")

// ErrNoDataClusters reports an entry that records a non-zero size but a first
// cluster of 0 or 1, neither of which exists in exFAT. There is nothing to
// locate, and deriving a cluster from the size would invent one.
var ErrNoDataClusters = errors.New("entry records a size but no first cluster")

// ErrNilContext reports a nil context passed to a cancellable entry point. A nil
// context is an error rather than a silent context.Background(): a caller who
// forgot to thread one through has an interruptible operation that cannot be
// interrupted, and silence hides that.
var ErrNilContext = errors.New("context is nil")

// ErrNilCallback reports a nil function passed to a walk.
var ErrNilCallback = errors.New("callback is nil")
