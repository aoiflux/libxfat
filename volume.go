package libxfat

// This file is the volume's own facts: the numbers the volume boot record
// records about itself. Every one of them was parsed and then unreachable before
// v2, which meant a consumer wanting the cluster size or the serial number had to
// re-parse the boot sector it had just handed to this library.
//
// They are all reads of fields fixed by parseVBR and never written again, so none
// of them takes the metadata lock and all of them are safe to call concurrently.
// VolumeLabel is the exception and says so.

// VolumeSerialNumber is the VolumeSerialNumber field of the boot sector.
//
// It is not a unique identifier and is not derived from the hardware: formatters
// generate it, usually from the clock at format time, and nothing prevents two
// volumes carrying the same value. It is evidence that two images are or are not
// the same volume only in the weak sense that a difference is conclusive and a
// match is not.
func (e *ExFAT) VolumeSerialNumber() uint32 {
	return e.vbr.serialNumber
}

// FilesystemRevision is the version of the exFAT specification the volume claims
// to conform to. Every volume in the wild reports 1.0.
func (e *ExFAT) FilesystemRevision() (major, minor byte) {
	return byte(e.vbr.version >> 8), byte(e.vbr.version & 0xff)
}

// VolumeFlags is the raw VolumeFlags field. Prefer the named accessors below; this
// is here so a report can quote the field including any bit this library does not
// interpret.
func (e *ExFAT) VolumeFlags() uint16 {
	return e.vbr.volumeFlags
}

// ActiveFAT is the index of the FAT the volume says is in use: 0 for the first,
// 1 for the second.
//
// It is only ever 1 on a TexFAT volume, which has two FATs, and chain walks read
// the FAT it names - see ActiveFatOffset for the address that follows from it.
//
// The index reported here is what the volume recorded, even where that cannot be
// acted on: a volume claiming one FAT and an active index of 1 is malformed, and
// this returns the 1 it recorded while the walk keeps reading the only FAT that
// exists. What the volume says and what can be read from it are two facts, and
// this accessor is the first of them.
func (e *ExFAT) ActiveFAT() int {
	if e.vbr.volumeFlags&VOLUME_FLAG_ACTIVE_FAT != 0 {
		return 1
	}
	return 0
}

// ActiveFatOffset is the absolute byte offset of the FAT that chain walks
// actually read.
//
// It differs from FatOffset only on a TexFAT volume whose ActiveFAT flag selects
// the second table. Comparing the two tables is itself an evidentiary exercise -
// they are both readable as the $FAT1 and $FAT2 virtual entries - and this says
// which of them every cluster chain this library reports came from.
func (e *ExFAT) ActiveFatOffset() int64 {
	return int64(e.vbr.fatStart())
}

// VolumeDirty reports the VolumeDirty flag: the volume was mounted for writing and
// not cleanly unmounted.
//
// For an investigator this is direct evidence about how the volume was last
// detached, and a caveat on everything else the volume says: a dirty volume's
// allocation bitmap, directory records and FAT may be mid-update and need not
// agree with each other.
func (e *ExFAT) VolumeDirty() bool {
	return e.vbr.volumeFlags&VOLUME_FLAG_VOLUME_DIRTY != 0
}

// MediaFailure reports the MediaFailure flag: an implementation met read or write
// errors on this volume that it could not recover from. Clusters may therefore be
// marked bad, and data that was once present may no longer be readable.
func (e *ExFAT) MediaFailure() bool {
	return e.vbr.volumeFlags&VOLUME_FLAG_MEDIA_FAILURE != 0
}

// PercentInUse is the PercentInUse field, a rounded hint the formatter or the last
// writer recorded. It is not computed from the allocation bitmap and need not agree
// with it; AllocatedClusters and ClusterCount are the counted answer. Volumes that
// disagree wildly are common: a hint of 0 on a three-quarters-full volume says only
// that nothing has updated the field.
//
// The specification reserves 0xFF for "not available", so treat 255 as absent
// rather than as a percentage. The recorded byte is returned as it stands either
// way, because which value the volume holds is itself evidence.
func (e *ExFAT) PercentInUse() byte {
	return e.vbr.percentInUse
}

// Base is the absolute byte offset within the image at which this volume begins.
//
// It is what every offset this library returns is measured from - an entry's
// EntrySetOffset, a Range's StartByte, a region entry's RegionOffset - so those
// values can be read directly from the image the volume was opened over, with no
// further adjustment. Subtract this to get a volume-relative offset.
func (e *ExFAT) Base() int64 {
	return e.vbr.base
}

// PartitionOffset is the partition offset the volume records about itself, in
// sectors, which is not necessarily where the volume was actually found. Base is
// where it was found. The two disagree on a superfloppy image whose boot record was
// written as though it sat inside a partition table, which is why
// Source.IgnorePartitionOffset exists.
func (e *ExFAT) PartitionOffset() uint64 {
	return e.vbr.vbrOffset
}

// VolumeSize is the size of the volume in sectors, as recorded in the boot sector.
// Multiply by BytesPerSector for bytes. The image may be larger than this, and on a
// truncated image it may be smaller than the image.
func (e *ExFAT) VolumeSize() uint64 {
	return e.vbr.volumeSize
}

// BytesPerSector is the volume's sector size. It is not always 512: exFAT permits
// up to 4096, and a library or caller that assumes 512 mislocates every region on
// such a volume.
func (e *ExFAT) BytesPerSector() uint32 {
	return e.vbr.sectorSize
}

// SectorsPerCluster is the cluster size expressed in sectors. ClusterSize is the
// same quantity in bytes.
func (e *ExFAT) SectorsPerCluster() uint32 {
	return e.vbr.sectorsPerCluster
}

// ClusterCount is the number of clusters in the cluster heap, as recorded in the
// boot sector. Valid cluster numbers are 2 through ClusterCount+1 inclusive, since
// exFAT has no cluster 0 or 1.
func (e *ExFAT) ClusterCount() uint32 {
	return e.vbr.nbClusters
}

// RootDirCluster is the first cluster of the root directory.
//
// exFAT addresses its root like any other directory, so this is a real cluster
// number and not a sentinel. It is the value Walk reports as the parent cluster of
// the root's children, and so the value a FileID carries for them.
func (e *ExFAT) RootDirCluster() uint32 {
	return e.vbr.rootDirCluster
}

// FatOffset is the absolute byte offset of the first FAT within the image.
func (e *ExFAT) FatOffset() int64 {
	return int64(e.vbr.firstFat)
}

// FatSize is the size of one FAT in bytes.
func (e *ExFAT) FatSize() uint64 {
	return e.vbr.fatBytes()
}

// FatCount is the number of FATs on the volume: 1 normally, 2 on a TexFAT volume.
// See ActiveFAT.
func (e *ExFAT) FatCount() byte {
	return e.vbr.numberOfFats
}

// ClusterHeapOffset is the absolute byte offset of the cluster heap within the
// image - where cluster 2 begins.
func (e *ExFAT) ClusterHeapOffset() int64 {
	return int64(e.vbr.dataAreaStart)
}

// VolumeLabel is the volume's label, or the empty string if it has none.
//
// The label is not in the boot sector. It is a directory record in the root
// directory, so answering this needs the root read, and this reads it if that has
// not happened yet - which is why it can fail where every other accessor in this
// file cannot.
//
// That lazy read is the point. GetVolumeLabel, which this replaces, returned
// whatever had been published so far: the empty string before the first
// ReadRootDir and the label afterwards, so the same call on the same volume gave
// two different answers depending on what the caller had happened to do first, and
// neither was flagged as incomplete. Here an empty string means the volume has no
// label, and not knowing yet is an error.
//
// Unlike the rest of this file this takes the metadata lock, and so is safe to call
// from several goroutines but is not free.
func (e *ExFAT) VolumeLabel() (string, error) {
	if !e.rootHasBeenParsed() {
		if e.vbr.dimage == nil || e.vbr.rootDirCluster == 0 {
			return "", ErrNilReader
		}
		if _, err := e.ReadRootDir(); err != nil {
			return "", err
		}
	}

	e.metaMu.RLock()
	defer e.metaMu.RUnlock()
	return e.vbr.volumeLabel, nil
}
