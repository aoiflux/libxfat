package libxfat

// This file is the library's own report: one JSON document describing a volume
// and everything a walk of it found. It exists because a consumer correlating
// several filesystems needs one shape it can read from all of them, and because
// every finding this library is careful about - a hypothesised extent, a broken
// chain, a record that contradicts itself - has to survive being written down.
//
// Two rules govern the types below, and both are the opposite of what a compact
// document would want. Identity scalars are never omitted when zero, because a
// missing key is indistinguishable from a real zero and every one of these has a
// meaningful zero. And no provenance flag is ever omitted when false, because
// "these ranges did not come from a FAT walk" and "this version does not emit that
// field" mean entirely different things to a reader.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
)

// ExFATReport is a volume-level view of an exFAT image, in the shape the sibling
// filesystem libraries emit so that reports from several filesystems can be
// consumed together.
//
// StartOffset and EndOffset bound the volume within the reader it was opened over.
// StartOffset is the volume's base rather than zero, and EndOffset is exclusive:
// every offset in this document is absolute within that reader, so the bounds are
// stated the same way. The sibling libfat report measures from the volume instead,
// which is the one difference to watch when reading both.
type ExFATReport struct {
	Name        string      `json:"name"`
	StartOffset int64       `json:"start_offset"`
	EndOffset   int64       `json:"end_offset"`
	Filesystem  ExFATMeta   `json:"exfat_meta"`
	Files       []ExFATFile `json:"files"`
}

// ExFATMeta describes the volume's geometry, what it says about its own state, and
// what it is able to record at all.
//
// Type, BlockSize and Offset are the fields every report in this family carries.
// The rest are exFAT-specific and are here because they are evidence: a volume that
// was not cleanly unmounted, one that records a media failure, or one whose
// recorded fullness disagrees with its own allocation bitmap is a different kind of
// finding from a clean one, and a report that omitted those would describe a
// healthier image than the one on disk.
type ExFATMeta struct {
	// Type is exFAT, or TexFAT for a volume carrying the second FAT.
	Type string `json:"type"`
	// BlockSize is the cluster size in bytes, which is the allocation unit every
	// extent in this report is a multiple of.
	BlockSize int `json:"block_size"`
	// Offset is the absolute byte offset of the cluster heap - where cluster 2
	// begins.
	Offset int64 `json:"offset"`

	SectorSize        int    `json:"sector_size"`
	SectorsPerCluster int    `json:"sectors_per_cluster"`
	ClusterCount      uint32 `json:"cluster_count"`
	RootDirCluster    uint32 `json:"root_dir_cluster"`
	VolumeLabel       string `json:"volume_label,omitempty"`
	VolumeSerial      uint32 `json:"volume_serial"`
	Revision          string `json:"revision"`

	// Base is where the volume boot record was actually found, and PartitionOffset
	// is where the volume says it sits, in sectors. The two disagree on a
	// superfloppy image whose boot record was written as though it lay inside a
	// partition table, which is why Source.IgnorePartitionOffset exists; the
	// disagreement itself is worth recording.
	Base            int64  `json:"base"`
	PartitionOffset uint64 `json:"partition_offset"`
	VolumeSizeBytes int64  `json:"volume_size_bytes"`

	FatCount        byte  `json:"fat_count"`
	FatOffset       int64 `json:"fat_offset"`
	FatSizeBytes    int64 `json:"fat_size_bytes"`
	ActiveFAT       int   `json:"active_fat"`
	ActiveFatOffset int64 `json:"active_fat_offset"`

	// VolumeFlags is the raw field; VolumeDirty and MediaFailure are the two bits
	// of it that bear on whether the rest of this document can be trusted. A dirty
	// volume's bitmap, directory records and FAT may be mid-update and need not
	// agree with each other.
	VolumeFlags  uint16 `json:"volume_flags"`
	VolumeDirty  bool   `json:"volume_dirty"`
	MediaFailure bool   `json:"media_failure"`

	// PercentInUse is the hint recorded in the boot sector, and
	// PercentInUseAvailable is false when the volume recorded the reserved 0xFF
	// meaning "not available" - in which case PercentInUse is not a percentage and
	// must not be read as one.
	PercentInUse          byte `json:"percent_in_use"`
	PercentInUseAvailable bool `json:"percent_in_use_available"`
	// AllocatedClusters is counted from the allocation bitmap rather than taken
	// from PercentInUse, and the two routinely disagree: a hint of 0 on a
	// three-quarters-full volume says only that nothing updated the field.
	// BitmapError carries the reason when the bitmap could not be counted, in
	// which case AllocatedClusters is zero because it is unknown, not because the
	// volume is empty.
	AllocatedClusters uint32 `json:"allocated_clusters"`
	BitmapError       string `json:"bitmap_error,omitempty"`

	// Capabilities is what this format records, as opposed to what this volume
	// happens to hold. It is embedded in the report so that a document read back
	// later still says that exFAT keeps no metadata-change time and no reusable
	// file identity - facts a consumer would otherwise have to know out of band.
	Capabilities Capabilities `json:"capabilities"`
}

// ExFATFile is one entry as a report row.
//
// The identity fields are what make two reports of the same volume comparable. A
// path is not an identity: it changes when a file or any of its ancestors is
// renamed, and it is reused when a new file takes an old name. ParentFirstCluster
// and EntrySlotIndex are the closest exFAT comes to one - which directory, and
// which slot of it - but exFAT has no generation or reuse counter of the kind NTFS
// and ext carry, so the pair is an address rather than an identity, and a slot
// reused after a deletion carries its predecessor's exactly. FirstCluster and the
// creation time are the corroborating signals. See FileID for the full account, and
// Capabilities.StableFileIdentity for the same fact stated so that a consumer can
// branch on it.
//
// None of the identity scalars is omitted when zero: slot 0 is the first slot of
// every directory, and an EntryAbsoluteOffset of -1 is the positive statement that
// the record could not be located. Identified is what separates a real identity
// from a pair of zeroes.
//
// The equivalent libfat key for Path is "filename"; the two libraries otherwise
// name these the same.
type ExFATFile struct {
	Path string `json:"path"`
	// Name is the name as recorded on the volume, with nothing appended - not even
	// for a deleted entry. SyntheticName marks the exception: an entry whose name
	// records held nothing usable carries a placeholder the library invented.
	Name string `json:"name"`
	// Type is file, directory, metadata, region or virtual.
	Type string `json:"type"`

	ParentFirstCluster uint32 `json:"parent_first_cluster"`
	EntrySlotIndex     uint32 `json:"entry_slot_index"`
	// Identified is true when the two fields above form a usable FileID. It is
	// false for the synthetic entries, which have no directory record, and for
	// records carved out of unallocated space, whose parent directory is gone.
	Identified   bool   `json:"identified"`
	FirstCluster uint32 `json:"first_cluster"`
	// EntryAbsoluteOffset is the absolute image offset of the entry set's primary
	// record, resolved through the parent directory's own extents so that it is
	// correct even when that directory is fragmented, and -1 when it could not be
	// resolved. Unlike EntrySlotIndex it is physical, and it does not survive the
	// parent directory being relocated.
	EntryAbsoluteOffset int64 `json:"entry_absolute_offset"`

	// RecordType is the raw directory entry type byte: 0x85 for a file or
	// directory, 0x05 for a deleted one, 0x81 and 0x82 for the allocation bitmap
	// and up-case table, 0xFF for an entry the library synthesised.
	RecordType byte   `json:"record_type"`
	Attributes uint16 `json:"attributes"`
	// SecondaryFlags is the raw GeneralSecondaryFlags byte, quoted whole so that a
	// bit this library does not interpret still reaches the reader.
	// AllocationPossible is its bit 0; see Layout.AllocationContradiction for what
	// a clear bit means next to a first cluster.
	SecondaryFlags     byte `json:"secondary_flags"`
	AllocationPossible bool `json:"allocation_possible"`

	// Size is the recorded DataLength and ValidSize the recorded ValidDataLength.
	// Bytes between them are allocated and readable but were never written by this
	// file; Unwritten locates them when asked for.
	Size      int64 `json:"size"`
	ValidSize int64 `json:"valid_size"`

	IsDirectory  bool `json:"is_directory"`
	IsFragmented bool `json:"is_fragmented"`
	IsDeleted    bool `json:"is_deleted"`
	// IsRecovered marks a record carved out of a cluster no directory references,
	// rather than one reached by following names. Its path is the library's
	// invention; see RecoveredPath.
	IsRecovered bool `json:"is_recovered"`
	// IsVirtual marks an entry the library synthesised rather than read: the
	// metadata regions and the container for carved records.
	IsVirtual     bool `json:"is_virtual"`
	SyntheticName bool `json:"synthetic_name"`
	// ClusterAllocated is the allocation bitmap's opinion of the first cluster. On
	// a deleted entry a true means the cluster has been handed to a later file and
	// the content is most likely gone. On a live entry a false is a finding in its
	// own right: the volume lists a file whose own bitmap says its first cluster
	// is free.
	ClusterAllocated bool `json:"cluster_allocated"`

	// ChecksumChecked and ChecksumVerified report the entry set checksum. Checked
	// is false in optimistic mode and for synthetic entries, where the comparison
	// never happened, so Verified is only meaningful alongside it.
	ChecksumChecked  bool `json:"checksum_checked"`
	ChecksumVerified bool `json:"checksum_verified"`

	Timestamps Timestamps `json:"timestamps"`

	// Layout records how Fragments was derived. It is the part of this row that
	// separates a fact from a hypothesis, and it is never omitted.
	Layout    FragmentProvenance `json:"layout"`
	Fragments []FileFragment     `json:"fragments"`

	// Slack is the unused tail of the file's last cluster, present only when
	// ReportOptions.IncludeSlack is set and the file does not end on a cluster
	// boundary. Unwritten is the allocated tail past ValidSize, present only when
	// ReportOptions.IncludeUnwritten is set and there is one.
	Slack     *FileFragment  `json:"slack,omitempty"`
	Unwritten []FileFragment `json:"unwritten,omitempty"`
}

// FileFragment is one contiguous on-disk span of a file, converted from Range.
//
// EndOffset is exclusive - it is Range.EndByte, one past the last byte - which
// matches this package's Range and the equivalent types in the FAT, NTFS and XFS
// libraries. The ext library's FileFragment is inclusive; if you consume both, that
// is the difference to watch.
type FileFragment struct {
	StartOffset int64 `json:"start_offset"`
	EndOffset   int64 `json:"end_offset"`
	// FileOffset is where this span begins within the file, so consecutive
	// fragments tile the file without gaps.
	FileOffset int64 `json:"file_offset"`
	Length     int64 `json:"length"`

	// Sparse is always false on exFAT, which has no sparse allocation and backs
	// every run with real clusters. The field exists so that a consumer can treat
	// these fragments uniformly with those from filesystems that do have holes. It
	// is not the same as never-written; see Unwritten.
	Sparse bool `json:"sparse"`

	// StartCluster and ClusterCount are the exFAT-native addressing of the same
	// span, for cross-referencing against the FAT and the allocation bitmap. Both
	// are zero for a region that is not cluster-addressed at all - the $MBR and
	// $FAT entries.
	StartCluster uint32 `json:"start_cluster"`
	ClusterCount uint32 `json:"cluster_count"`
}

// FragmentProvenance carries FragmentResult's flags into the JSON.
//
// These are the distinguishing content of a libxfat report and none of them is
// omitted when false. A chain_walked of false is the statement that these fragments
// did not come from a FAT chain; dropping the key would leave a consumer unable to
// tell that from a field this version did not emit, and a hypothesised extent would
// become indistinguishable from a verified one.
type FragmentProvenance struct {
	// ChainWalked is true when the fragments came from an actual FAT chain walk. It
	// is false for a stream the volume declared contiguous, for the region entries,
	// which are not cluster-backed, and for a deleted entry, whose chain has been
	// freed.
	ChainWalked bool `json:"chain_walked"`
	// NoFatChain is true when the record itself declared the stream contiguous, so
	// the extent came from size arithmetic and the FAT was never consulted. Unlike
	// Assumed this is not a guess the library made: the volume stated it, and
	// deleting a file does not erase the statement.
	NoFatChain bool `json:"no_fat_chain"`
	// Assumed is true when the fragments were synthesised under
	// FragmentOptions.AssumeContiguous rather than read from the FAT or declared by
	// the volume. Data located through them is a hypothesis, not a fact.
	Assumed bool `json:"assumed"`
	// Truncated is true when the fragments cover less than the entry's recorded
	// size.
	Truncated bool `json:"truncated"`
	// ChainBroken is true when the walk stopped on a free, bad or out-of-heap FAT
	// entry rather than a proper end-of-chain marker.
	ChainBroken bool `json:"chain_broken"`
	// LoopDetected is true when the chain revisited a cluster.
	LoopDetected bool `json:"loop_detected"`
	// FirstClusterReallocated is true for a deleted entry whose first cluster is
	// now marked in use, meaning its content was most likely overwritten.
	FirstClusterReallocated bool `json:"first_cluster_reallocated"`
	// AllocationContradiction is true when the record named a first cluster while
	// its own flags said no allocation was possible. The fragments were located
	// from fields the specification defines as undefined.
	AllocationContradiction bool `json:"allocation_contradiction"`

	ClustersWalked uint32 `json:"clusters_walked"`
	BytesCovered   int64  `json:"bytes_covered"`
	// ValidBytes is the recorded ValidDataLength, repeated here beside the extents
	// it qualifies.
	ValidBytes int64 `json:"valid_bytes"`

	// Error is the reason no fragments could be derived, when there is one. The row
	// is kept either way: an entry whose extent could not be resolved is still an
	// entry, and why it failed is itself a finding.
	Error string `json:"error,omitempty"`
}

// ReportOptions controls what a report collects.
//
// The zero value is the reachable, live tree with FAT-verified extents. Every field
// either widens the search or relaxes the evidence, and none is on by default.
type ReportOptions struct {
	// IncludeDeleted, DescendDeletedDirectories and IncludeRecovered are passed
	// through to the walk; see WalkOptions, which documents what each one costs and
	// what it risks.
	IncludeDeleted            bool
	DescendDeletedDirectories bool
	IncludeRecovered          bool

	// Fragments is passed to FragmentOffsetsWithOptions for every row. Its zero
	// value walks the FAT and never fabricates an offset. Setting AssumeContiguous
	// fills in extents for deleted entries under an assumption; rows produced that
	// way carry layout.assumed, and no report constructor sets it for you.
	Fragments FragmentOptions

	// IncludeSlack adds each file's cluster slack to its row, and IncludeUnwritten
	// the allocated tail past ValidDataLength. Each costs one extra resolution per
	// row.
	IncludeSlack     bool
	IncludeUnwritten bool

	// MaxDepth bounds the walk; see WalkOptions.MaxDepth.
	MaxDepth int
}

// ExFATReportSummary is the aggregate view of a report.
type ExFATReportSummary struct {
	Total      int `json:"total"`
	Deleted    int `json:"deleted"`
	Recovered  int `json:"recovered"`
	Fragmented int `json:"fragmented"`
	// Assumed counts rows whose extents are hypotheses rather than facts, and
	// Truncated those whose extents account for less than the recorded size.
	Assumed   int `json:"assumed"`
	Truncated int `json:"truncated"`
	// Contradictory counts rows whose record named a first cluster while declaring
	// no allocation possible, and Unresolved those whose extents could not be
	// derived at all.
	Contradictory int            `json:"contradictory"`
	Unresolved    int            `json:"unresolved"`
	TypeCounts    map[string]int `json:"type_counts"`
	TotalSize     int64          `json:"total_size"`
}

// Report returns a report of the volume's live, reachable tree.
//
// It is ReportWithOptions with the zero ReportOptions. name identifies the image in
// the report and is not read from the volume.
func (e *ExFAT) Report(name string) (*ExFATReport, error) {
	return e.ReportWithOptions(name, ReportOptions{})
}

// ReportDeep returns a report that also covers deleted records and the directory
// data no path reaches.
//
// Deep means more places searched, never weaker evidence. It sets IncludeDeleted,
// DescendDeletedDirectories and IncludeRecovered, and deliberately does not set
// Fragments.AssumeContiguous: a report labelled deep that silently contained
// hypothesised extents would be the worst possible default, because the caller who
// most wants recovery data is the one least able to tell a reconstruction from a
// fact. Ask for that explicitly through ReportWithOptions, and read layout.assumed
// on every row.
func (e *ExFAT) ReportDeep(name string) (*ExFATReport, error) {
	return e.ReportWithOptions(name, ReportOptions{
		IncludeDeleted:            true,
		DescendDeletedDirectories: true,
		IncludeRecovered:          true,
		IncludeSlack:              true,
		IncludeUnwritten:          true,
	})
}

// ReportWithOptions returns a report of what the options select.
//
// It is ReportWithOptionsContext with context.Background().
func (e *ExFAT) ReportWithOptions(name string, opts ReportOptions) (*ExFATReport, error) {
	return e.ReportWithOptionsContext(context.Background(), name, opts)
}

// ReportWithOptionsContext returns a report of what the options select, with
// cancellation.
//
// A report walks the whole tree and resolves every row's extents, so it is worth
// interrupting on a large image. On cancellation the rows gathered so far are
// returned alongside ctx.Err(), following this package's rule that a partial result
// is reported rather than discarded. A caller wanting all-or-nothing discards the
// report on any non-nil error.
func (e *ExFAT) ReportWithOptionsContext(ctx context.Context, name string, opts ReportOptions) (*ExFATReport, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}

	report := &ExFATReport{
		Name:        name,
		StartOffset: e.Base(),
		EndOffset:   e.Base() + volumeBytes(e),
		Files:       []ExFATFile{},
	}

	// The walk reads the root first, which is what publishes the volume label and
	// the $BitMap stream the meta block wants, so the meta is built afterwards.
	err := e.WalkWithOptions(ctx, WalkOptions{
		IncludeDeleted:            opts.IncludeDeleted,
		DescendDeletedDirectories: opts.DescendDeletedDirectories,
		IncludeRecovered:          opts.IncludeRecovered,
		MaxDepth:                  opts.MaxDepth,
	}, func(path string, parentFirstCluster uint32, entry Entry) error {
		report.Files = append(report.Files, e.reportFile(path, parentFirstCluster, entry, opts))
		return nil
	})

	report.Filesystem = e.reportMeta()
	if err != nil {
		return report, err
	}
	return report, nil
}

// volumeBytes is the volume's recorded size in bytes. VolumeSize is in sectors,
// and a crafted value could overflow the multiplication, so a size that will not
// fit is reported as unknown rather than as a wrapped number.
func volumeBytes(e *ExFAT) int64 {
	size := e.VolumeSize()
	sector := uint64(e.BytesPerSector())
	if sector == 0 || size > uint64(math.MaxInt64)/sector {
		return 0
	}
	return int64(size * sector)
}

func (e *ExFAT) reportMeta() ExFATMeta {
	major, minor := e.FilesystemRevision()
	meta := ExFATMeta{
		Type:                  volumeType(e),
		BlockSize:             int(e.ClusterSize()),
		Offset:                e.ClusterHeapOffset(),
		SectorSize:            int(e.BytesPerSector()),
		SectorsPerCluster:     int(e.SectorsPerCluster()),
		ClusterCount:          e.ClusterCount(),
		RootDirCluster:        e.RootDirCluster(),
		VolumeSerial:          e.VolumeSerialNumber(),
		Revision:              string(rune('0'+major)) + "." + string(rune('0'+minor)),
		Base:                  e.Base(),
		PartitionOffset:       e.PartitionOffset(),
		VolumeSizeBytes:       volumeBytes(e),
		FatCount:              e.FatCount(),
		FatOffset:             e.FatOffset(),
		ActiveFAT:             e.ActiveFAT(),
		ActiveFatOffset:       e.ActiveFatOffset(),
		VolumeFlags:           e.VolumeFlags(),
		VolumeDirty:           e.VolumeDirty(),
		MediaFailure:          e.MediaFailure(),
		PercentInUse:          e.PercentInUse(),
		PercentInUseAvailable: e.PercentInUse() != percentInUseUnavailable,
		Capabilities:          e.Capabilities(),
	}
	if fatSize := e.FatSize(); fatSize <= uint64(math.MaxInt64) {
		meta.FatSizeBytes = int64(fatSize)
	}
	// A label this volume has not got is an empty string either way, and a root
	// that could not be read has already failed the walk.
	if label, err := e.VolumeLabel(); err == nil {
		meta.VolumeLabel = label
	}
	// Counted rather than taken from PercentInUse. The failure is recorded instead
	// of discarded: a volume whose bitmap cannot be read is a finding, and a zero
	// with no explanation beside it would read as an empty volume.
	if allocated, err := e.AllocatedClusters(); err == nil {
		meta.AllocatedClusters = allocated
	} else {
		meta.BitmapError = err.Error()
	}
	return meta
}

// percentInUseUnavailable is the value the specification reserves to mean that the
// PercentInUse hint is not available, as opposed to a volume that is 255% full.
const percentInUseUnavailable = 0xFF

func volumeType(e *ExFAT) string {
	if e.FatCount() > 1 {
		return "TexFAT"
	}
	return "exFAT"
}

// reportFile builds one row. It never fails: an entry whose extents cannot be
// resolved keeps its row with the reason recorded in Layout.Error, because the
// entry is evidence whether or not its data can be located.
func (e *ExFAT) reportFile(path string, parentFirstCluster uint32, entry Entry, opts ReportOptions) ExFATFile {
	row := ExFATFile{
		Path:                path,
		Name:                entry.Name(),
		Type:                entryType(entry),
		FirstCluster:        entry.FirstCluster(),
		EntryAbsoluteOffset: -1,
		RecordType:          entry.EntryType(),
		Attributes:          entry.Attributes(),
		SecondaryFlags:      entry.SecondaryFlags(),
		AllocationPossible:  entry.AllocationPossible(),
		IsDirectory:         entry.IsDir(),
		IsDeleted:           entry.IsDeleted(),
		IsRecovered:         strings.HasPrefix(path, RecoveredPath+"/"),
		IsVirtual:           entry.IsVirtualEntry(),
		SyntheticName:       entry.HasSyntheticName(),
		ChecksumChecked:     entry.NameChecksumVerified() || entry.NameChecksumMismatch(),
		ChecksumVerified:    entry.NameChecksumVerified(),
		Timestamps:          entry.Timestamps(),
		Fragments:           []FileFragment{},
	}
	// Sizes are recorded as unsigned 64-bit fields, so a crafted value can exceed
	// what JSON consumers and this type can express. Leaving such a size at zero
	// would understate it; the extents below still say what could be located.
	if entry.Size() <= math.MaxInt64 {
		row.Size = int64(entry.Size())
	}
	if entry.ValidDataSize() <= math.MaxInt64 {
		row.ValidSize = int64(entry.ValidDataSize())
	}
	if offset, ok := entry.EntrySetOffset(); ok {
		row.EntryAbsoluteOffset = offset
	}
	if id, ok := e.FileID(entry); ok {
		row.ParentFirstCluster = id.ParentFirstCluster
		row.EntrySlotIndex = id.EntrySlotIndex
		row.Identified = true
	} else {
		// Report the address the walk and the record did supply, marked as not an
		// identity. Inventing a zero parent would collide every unidentifiable row
		// onto one key.
		row.ParentFirstCluster = parentFirstCluster
		row.EntrySlotIndex = entry.EntrySlotIndex()
	}

	e.resolveRow(&row, entry, opts)
	return row
}

// resolveRow fills in a row's extents and the bitmap's opinion of its first
// cluster.
func (e *ExFAT) resolveRow(row *ExFATFile, entry Entry, opts ReportOptions) {
	result, err := e.FragmentOffsetsWithOptions(entry, opts.Fragments)
	if err != nil {
		row.Layout.Error = err.Error()
		return
	}
	row.Layout = FragmentProvenance{
		ChainWalked:             result.ChainWalked,
		NoFatChain:              result.NoFatChain,
		Assumed:                 result.Assumed,
		Truncated:               result.Truncated,
		ChainBroken:             result.ChainBroken,
		LoopDetected:            result.LoopDetected,
		FirstClusterReallocated: result.FirstClusterReallocated,
		AllocationContradiction: result.AllocationContradiction,
		ClustersWalked:          result.ClustersWalked,
		BytesCovered:            result.BytesCovered,
		ValidBytes:              result.ValidBytes,
	}
	row.Fragments = toFileFragments(result.Ranges, 0)
	row.IsFragmented = IsFragmented(result.Ranges)

	// A deleted entry's first cluster was already tested against the bitmap to
	// produce FirstClusterReallocated, so that answer is reused rather than paid
	// for twice. A live entry costs one lookup, and an unreadable bitmap leaves
	// the field false, which is a statement about the library's knowledge rather
	// than about the cluster.
	switch {
	case entry.IsDeleted():
		row.ClusterAllocated = result.FirstClusterReallocated
	case entry.IsRegion():
		// Regions lie outside the cluster heap; the bitmap has no opinion.
	default:
		if allocated, err := e.IsClusterAllocated(entry.FirstCluster()); err == nil {
			row.ClusterAllocated = allocated
		}
	}

	if opts.IncludeSlack {
		if slack, ok, serr := e.SlackRange(entry); serr == nil && ok {
			frag := toFileFragments([]Range{slack}, row.Size)[0]
			row.Slack = &frag
		}
	}
	if opts.IncludeUnwritten {
		if unwritten, uerr := e.UnwrittenRanges(entry); uerr == nil && len(unwritten) > 0 {
			row.Unwritten = toFileFragments(unwritten, row.Layout.ValidBytes)
		}
	}
}

// entryType names what kind of thing the row describes.
//
// The order matters: the synthetic entries are also special files, so they are
// classified before the test that would fold them in with the volume's own
// metadata streams.
func entryType(entry Entry) string {
	switch {
	case entry.IsRegion():
		return "region"
	case entry.IsVirtualEntry():
		return "virtual"
	case entry.IsBitmapUpcase(), entry.IsSpecialFile():
		return "metadata"
	case entry.IsDir():
		return "directory"
	default:
		return "file"
	}
}

// toFileFragments converts the package's Range list into the report's extent type,
// filling in the file-relative offset that Range does not carry. firstOffset is
// where the first run begins within the file, which is zero for a file's own
// extents and the boundary the run follows for slack and unwritten tails.
func toFileFragments(ranges []Range, firstOffset int64) []FileFragment {
	out := make([]FileFragment, 0, len(ranges))
	fileOffset := firstOffset
	for _, r := range ranges {
		out = append(out, FileFragment{
			StartOffset:  r.StartByte,
			EndOffset:    r.EndByte(),
			FileOffset:   fileOffset,
			Length:       r.Length,
			Sparse:       r.Sparse,
			StartCluster: r.StartCluster,
			ClusterCount: r.ClusterCount,
		})
		fileOffset += r.Length
	}
	return out
}

// WriteReport writes a report of the live tree to w as indented JSON.
func (e *ExFAT) WriteReport(name string, w io.Writer) error {
	return e.WriteReportWithOptions(name, ReportOptions{}, w)
}

// WriteReportDeep writes a ReportDeep to w as indented JSON.
func (e *ExFAT) WriteReportDeep(name string, w io.Writer) error {
	return e.WriteReportWithOptions(name, ReportOptions{
		IncludeDeleted:            true,
		DescendDeletedDirectories: true,
		IncludeRecovered:          true,
		IncludeSlack:              true,
		IncludeUnwritten:          true,
	}, w)
}

// WriteReportWithOptions writes a report to w as indented JSON.
//
// It is WriteReportWithOptionsContext with context.Background().
func (e *ExFAT) WriteReportWithOptions(name string, opts ReportOptions, w io.Writer) error {
	return e.WriteReportWithOptionsContext(context.Background(), name, opts, w)
}

// WriteReportWithOptionsContext writes a report to w as indented JSON, with
// cancellation.
//
// Nothing is written when the report could not be completed. A truncated listing
// that looks complete is worse than none, and unlike the in-memory forms there is
// no flag on the written document to say otherwise.
func (e *ExFAT) WriteReportWithOptionsContext(ctx context.Context, name string, opts ReportOptions, w io.Writer) error {
	if w == nil {
		return errors.New("writer is nil")
	}
	report, err := e.ReportWithOptionsContext(ctx, name, opts)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

// Summary returns the report's aggregate counters.
func (r *ExFATReport) Summary() ExFATReportSummary {
	summary := ExFATReportSummary{TypeCounts: make(map[string]int)}
	for _, f := range r.Files {
		summary.Total++
		summary.TypeCounts[f.Type]++
		summary.TotalSize += f.Size
		if f.IsDeleted {
			summary.Deleted++
		}
		if f.IsRecovered {
			summary.Recovered++
		}
		if f.IsFragmented {
			summary.Fragmented++
		}
		if f.Layout.Assumed {
			summary.Assumed++
		}
		if f.Layout.Truncated {
			summary.Truncated++
		}
		if f.Layout.AllocationContradiction {
			summary.Contradictory++
		}
		if f.Layout.Error != "" {
			summary.Unresolved++
		}
	}
	return summary
}

// FilterFiles returns the rows fn accepts.
func (r *ExFATReport) FilterFiles(fn func(ExFATFile) bool) []ExFATFile {
	var out []ExFATFile
	for _, f := range r.Files {
		if fn(f) {
			out = append(out, f)
		}
	}
	return out
}

// FilesByType returns the rows of the given type: file, directory, metadata,
// region or virtual.
func (r *ExFATReport) FilesByType(t string) []ExFATFile {
	return r.FilterFiles(func(f ExFATFile) bool { return f.Type == t })
}

// DeletedFiles returns the rows whose records carry the deletion marker.
func (r *ExFATReport) DeletedFiles() []ExFATFile {
	return r.FilterFiles(func(f ExFATFile) bool { return f.IsDeleted })
}

// RecoveredFiles returns the rows carved out of clusters no path reaches.
func (r *ExFATReport) RecoveredFiles() []ExFATFile {
	return r.FilterFiles(func(f ExFATFile) bool { return f.IsRecovered })
}

// FragmentedFiles returns the rows occupying more than one run.
func (r *ExFATReport) FragmentedFiles() []ExFATFile {
	return r.FilterFiles(func(f ExFATFile) bool { return f.IsFragmented })
}

// AssumedFiles returns the rows whose extents are hypotheses rather than facts,
// which is the set a caller must not treat as located data without saying so.
func (r *ExFATReport) AssumedFiles() []ExFATFile {
	return r.FilterFiles(func(f ExFATFile) bool { return f.Layout.Assumed })
}
