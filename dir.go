package libxfat

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func (e *ExFAT) hasBitmapEntry() bool {
	return e.vbr.bitmapEntry.GetName() != "" && e.vbr.bitmapEntry.GetEntryCluster() != 0 && e.vbr.bitmapEntry.GetSize() != 0
}

func (e *ExFAT) ensureBitmapEntry() error {
	if e.hasBitmapEntry() {
		return nil
	}
	if e.vbr.dimage != nil && e.vbr.rootDirCluster != 0 {
		if _, err := e.ReadRootDir(); err != nil {
			return err
		}
	}
	if !e.hasBitmapEntry() {
		return ErrAllocationBitmapNotFound
	}
	return nil
}

func (e *ExFAT) resetSetAssembly() {
	e.setChecksum = 0
	e.expectedChecksum = 0
	e.expectedSC = 0
	e.expectedNameLen = 0
	e.nameUnits = nil
	e.setInUse = false
	e.sawStream = false
}

// beginEntrySet starts assembling a new file entry set from its primary record.
// The pending entry is cleared first: an earlier set that was abandoned partway
// through - a truncated directory, a damaged secondary - would otherwise leave
// its cluster and length behind for this one to inherit.
func (e *ExFAT) beginEntrySet(rec dirRecordView) {
	e.entry = Entry{}
	e.resetSetAssembly()

	e.setChecksum = exfatDirSetChecksumAdd(0, rec.data, true)
	e.setInUse = entryInUse(rec.typeByte())
	e.populateDirRecordDel(rec)
	e.expectedSC = int(e.entry.secondaryCount)
	e.expectedChecksum = uint16(rec.byteAt(2)) | (uint16(rec.byteAt(3)) << 8)
}

// secondaryBelongsToSet reports whether a secondary record's allocation state
// agrees with the primary that opened the set. Type validation masks off the
// in-use bit, so without this a 0x41 name record would be accepted into an
// allocated 0x85 set, splicing a deleted name onto a live file.
func (e *ExFAT) secondaryBelongsToSet(rec dirRecordView) bool {
	return e.entryState == ENTRY_STATE_85_SEEN && entryInUse(rec.typeByte()) == e.setInUse
}

// finishEntrySet completes the pending entry set and appends it to entries.
//
// The assembled name is always stored. A checksum mismatch says the set is
// damaged, which is a fact worth reporting about the entry - it is not a reason
// to replace the only copy of the name with an empty string, which is what this
// used to do in strict mode, silently rendering every entry on the volume
// nameless and every directory unreadable.
func (e *ExFAT) finishEntrySet(entries *[]Entry) {
	if e.expectedNameLen > 0 && len(e.nameUnits) > e.expectedNameLen {
		e.nameUnits = e.nameUnits[:e.expectedNameLen]
	}

	checked := !e.optimistic
	verified := e.expectedChecksum == e.setChecksum

	name := utf16UnitsToString(e.nameUnits)

	// A set whose name records are empty or all NUL leaves nothing to call the
	// entry by, and a directory with no name is treated as unreadable, so the
	// whole subtree below it would disappear without a word. An entry is located
	// by its cluster, not by its name, so there is no reason to lose it: stand a
	// placeholder in, keyed to the cluster so it is stable across runs, and
	// record that the name did not come off the disk.
	if name == "" {
		name = unnamedEntryName(e.entry.entryCluster)
		e.entry.nameSynthetic = true
	}

	e.entry.name = name
	e.entry.nameChecksumChecked = checked
	e.entry.nameChecksumVerified = verified
	e.entry.expectedSetChecksum = e.expectedChecksum
	e.entry.computedSetChecksum = e.setChecksum

	if e.entry.IsDeleted() {
		e.entry.name += DELETED
	}

	drop := checked && !verified && e.rejectChecksumMismatch
	if !drop {
		*entries = append(*entries, e.entry)
	}

	e.entry = Entry{}
	e.entryState = ENTRY_STATE_LAST_C1_SEEN
	e.resetSetAssembly()
}

func (e *ExFAT) clearParsedEntry() {
	e.entry = Entry{}
	e.entryState = ENTRY_STATE_START
	e.remainingSC = 0
	e.resetSetAssembly()
}

// GetAllocatedClusters function is experimental, it may not work correctly all the time
// It has been tested to work correctly if used directly after parsing root entries
func (e *ExFAT) GetAllocatedClusters() (uint32, error) {
	if err := e.ensureBitmapEntry(); err != nil {
		return 0, ErrAllocationBitmapNotFound
	}

	counter := bitmapCounter{}
	err := e.vbr.visitEntryData(e.vbr.bitmapEntry, func(_ uint32, chunk []byte) error {
		counter.write(chunk)
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, ErrEOF) {
		return 0, err
	}
	allocatedClusters := counter.count()
	return allocatedClusters, nil
}

// GetFreeClusters function is experimental, it may not work correctly all the time
// It has been tested to work correctly if used directly after parsing root entries
func (e *ExFAT) GetFreeClusters() (uint32, error) {
	allocatedClusters, err := e.GetAllocatedClusters()
	if err != nil {
		return 0, err
	}
	freeClusters := e.vbr.nbClusters - allocatedClusters
	return freeClusters, nil
}

func (e *ExFAT) GetClusterSize() uint64 {
	return e.vbr.clusterSize
}

// ExtractEntryContent writes the entry's content stream to dstpath.
//
// Alongside regular files it accepts the filesystem's own metadata: the
// $BitMap and $UpCase streams, and the $MBR, $FAT1 and $FAT2 regions. Those
// entries report IsInvalid, because they are not file entry sets, but they do
// have recoverable content and refusing them left them visible yet unreadable.
func (e *ExFAT) ExtractEntryContent(entry Entry, dstpath string) error {
	if entry.IsDeleted() {
		return ErrDeletedEntry
	}
	if !entry.IsRegion() && !entry.IsMetadataStream() && entry.IsInvalid() {
		return ErrInvalidEntry
	}
	return e.vbr.extractEntryContent(entry, dstpath)
}

func (e *ExFAT) ExtractAllFiles(rootEntries []Entry, dstdir string) error {
	err := e.getAllEntriesInfo(rootEntries, "/", dstdir, false, false, true)
	if err != nil {
		return err
	}

	fmt.Println("Done!")
	return nil
}

func (e *ExFAT) GetFullPathIndexableEntries(entries []Entry, path string) ([]Entry, error) {
	var retentries []Entry

	for _, entry := range entries {
		entry.name = path + entry.name

		if entry.IsIndexable() {
			retentries = append(retentries, entry)
		}

		subentries, err := e.ReadDir(entry)
		if err != nil {
			return nil, err
		}

		tempRet, err := e.GetFullPathIndexableEntries(subentries, entry.name+"/")
		if err != nil {
			return nil, err
		}
		retentries = append(retentries, tempRet...)
	}

	return retentries, nil
}

func (e *ExFAT) ShowAllEntriesInfo(rootEntries []Entry, path string, long, simple bool) error {
	return e.getAllEntriesInfo(rootEntries, path, "", long, simple, false)
}

func (e *ExFAT) getAllEntriesInfo(entries []Entry, path, dstdir string, long, simple, extract bool) error {
	for _, entry := range entries {
		err := e.processEntry(entry, path, dstdir, extract, long, simple)
		if err != nil {
			return err
		}

		subentries, err := e.ReadDir(entry)
		if err != nil {
			return err
		}

		err = e.getAllEntriesInfo(subentries, path+entry.name+"/", dstdir, long, simple, extract)
		if err != nil {
			return err
		}
	}

	return nil
}

func (e ExFAT) processEntry(entry Entry, path, dstdir string, extract, long, simple bool) error {
	if extract {
		relDir := strings.Trim(path, "/\\")
		dstpath := filepath.Join(dstdir, filepath.FromSlash(relDir), entry.name)

		if !entry.IsValid() || !entry.IsIndexed() {
			return nil
		}
		if entry.IsDir() {
			return os.MkdirAll(dstpath, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(dstpath), 0o755); err != nil {
			return err
		}
		return e.ExtractEntryContent(entry, dstpath)
	}

	entryString := getDirEntry(entry, path, long, simple)
	fmt.Println(entryString)

	return nil
}

// limit - 2,14,74,83,646 entries
func (e *ExFAT) GetIndexableEntries(rootEntries []Entry) ([]Entry, error) {
	return e.GetAllEntries(rootEntries, true)
}

// limit - 2,14,74,83,646 entries
func (e *ExFAT) GetAllEntries(rootEntries []Entry, indexable ...bool) ([]Entry, error) {
	var flag bool
	var err error
	var allEntries []Entry
	subEntries := rootEntries

	if len(indexable) > 0 {
		flag = indexable[0]
	}

	for {
		if subEntries == nil {
			break
		}

		for _, subEntry := range subEntries {
			if flag && subEntry.IsNotIndexable() {
				continue
			}
			allEntries = append(allEntries, subEntry)
		}

		subEntries, err = e.ReadDirs(subEntries)
		if err != nil {
			return nil, err
		}
	}

	return allEntries, err
}

func (e *ExFAT) ReadDirs(rootEntries []Entry) ([]Entry, error) {
	var entries []Entry

	for _, entry := range rootEntries {
		subentries, err := e.ReadDir(entry)
		if err != nil {
			return nil, err
		}
		entries = append(entries, subentries...)
	}

	return entries, nil
}

func (e *ExFAT) ReadDir(entry Entry) ([]Entry, error) {
	if entry.NonParsable() {
		return nil, nil
	}
	entries, err := e.readDirEntries(entry)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, ErrEOF) {
		return nil, err
	}
	return entries, err
}

func (e *ExFAT) ReadRootDir() ([]Entry, error) {
	entries, err := e.readRootDirEntries()
	if err != nil {
		return nil, err
	}
	entries = append(entries, e.createVirtualEntries()...)
	return entries, nil
}

// RecoverDeletedEntries scans unallocated clusters and attempts to parse
// deleted exFAT file entry sets (0x05/0x40/0x41), similar to TSK-style
// orphan/deleted discovery.
func (e *ExFAT) RecoverDeletedEntries() ([]Entry, error) {
	unallocated, err := e.getUnallocatedClusters()
	if err != nil {
		return nil, err
	}

	var deleted []Entry
	for _, cluster := range unallocated {
		clusterdata, err := e.vbr.readClusters(cluster, 1)
		if err != nil {
			return nil, err
		}
		deleted = append(deleted, e.parseDeletedDirEntries(clusterdata)...)
	}

	return deleted, nil
}

func (e *ExFAT) getUnallocatedClusters() ([]uint32, error) {
	if err := e.ensureBitmapEntry(); err != nil {
		return nil, err
	}

	var unallocated []uint32
	clusterIndex := uint32(0)
	err := e.vbr.visitEntryData(e.vbr.bitmapEntry, func(_ uint32, chunk []byte) error {
		for _, b := range chunk {
			for bit := 0; bit < 8 && clusterIndex < e.vbr.nbClusters; bit++ {
				allocated := (b & (1 << bit)) != 0
				if !allocated {
					unallocated = append(unallocated, uint32(FIRST_CLUSTER_NUMBER)+clusterIndex)
				}
				clusterIndex++
			}
			if clusterIndex >= e.vbr.nbClusters {
				return errStopClusterWalk
			}
		}
		return nil
	})
	if errors.Is(err, errStopClusterWalk) {
		return unallocated, nil
	}
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, ErrEOF) {
		return nil, err
	}

	return unallocated, nil
}

func (e *ExFAT) CountClusters(entry Entry) (int, error) {
	return e.vbr.countClusters(entry)
}

// GetClusterList method returns a list of all the clusters in a file
// end index of the last byte in the last cluster of the file
func (e *ExFAT) GetClusterList(entry Entry) ([]uint32, uint64, error) {
	return e.vbr.getClusterList(entry)
}
func (e *ExFAT) GetClusterOffset(cluster uint32) uint64 {
	return e.vbr.getClusterOffset(cluster)
}

func (e *ExFAT) GetUsedSpace() string {
	return fmt.Sprintf("%d%%", e.vbr.percentInUse)
}

func (e *ExFAT) resetDirParser() {
	e.initEntryState(nil, 0, 0, ENTRY_STATE_START)
	e.resetSetAssembly()
}

func (e *ExFAT) readDirEntries(entry Entry) ([]Entry, error) {
	var entries []Entry
	done := false
	e.resetDirParser()
	err := e.vbr.visitEntryData(entry, func(_ uint32, chunk []byte) error {
		if done {
			return nil
		}
		if e.parseDirChunk(chunk, &entries) {
			done = true
		}
		return nil
	})
	return entries, err
}

func (e *ExFAT) readRootDirEntries() ([]Entry, error) {
	var entries []Entry
	done := false
	e.resetDirParser()
	err := e.vbr.visitFatChain(e.vbr.rootDirCluster, func(_ uint32, chunk []byte) error {
		if done {
			return nil
		}
		if e.parseDirChunk(chunk, &entries) {
			done = true
		}
		return nil
	})
	return entries, err
}

func (e *ExFAT) parseDir(clusterdata []byte) []Entry {
	var entries []Entry
	e.resetDirParser()
	e.parseDirChunk(clusterdata, &entries)
	return entries
}

func (e *ExFAT) parseDeletedDirEntries(clusterdata []byte) []Entry {
	var entries []Entry
	e.resetDirParser()
	e.clusterdata = clusterdata

	for offset := 0; offset+EXFAT_DIRRECORD_SIZE <= len(clusterdata); offset += EXFAT_DIRRECORD_SIZE {
		rec, ok := newDirRecordView(clusterdata, offset)
		if !ok {
			break
		}
		e.offset = offset
		e.dirtype = rec.typeByte()

		if e.dirtype == 0 {
			e.clearParsedEntry()
			continue
		}

		if (e.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_FILEDIR {
			e.clearParsedEntry()
			if e.validateFileDentry(rec.data) {
				e.beginEntrySet(rec)
			}
			continue
		}

		if (e.dirtype&0x7f) == EXFAT_DIRRECORD_DEL_STREAM_EXT && e.secondaryBelongsToSet(rec) {
			if !e.sawStream && e.validateFileStreamDentry(rec.data) {
				e.setChecksum = exfatDirSetChecksumAdd(e.setChecksum, rec.data, false)
				e.populateDirRecordStreamSeen(rec)
				e.expectedNameLen = int(e.entry.nameLen)
				e.sawStream = true
			}
			continue
		}

		if (e.dirtype&0x7f) == EXFAT_DIRRECORD_DEL_FILENAME_EXT && e.secondaryBelongsToSet(rec) {
			if !e.validateFileNameDentry(rec.data) {
				continue
			}

			e.setChecksum = exfatDirSetChecksumAdd(e.setChecksum, rec.data, false)
			e.nameUnits = append(e.nameUnits, utf16leUnitsFromBytes(rec.bytes(2, EXFAT_DIRRECORD_SIZE), 15)...)

			if e.remainingSC < 1 {
				continue
			}
			e.remainingSC--
			if e.remainingSC != 0 {
				continue
			}

			// A set with no stream extension has no cluster, no length and no
			// name length; emitting it would invent a file that is not there.
			if !e.sawStream {
				e.clearParsedEntry()
				continue
			}

			e.finishEntrySet(&entries)
			e.clearParsedEntry()
		}
	}

	return entries
}

func (e *ExFAT) parseDirChunk(clusterdata []byte, entries *[]Entry) bool {
	e.clusterdata = clusterdata
	e.offset = 0

	for e.offset < len(clusterdata) {
		if clusterdata[e.offset] == 0 {
			return true
		}

		rec, ok := newDirRecordView(clusterdata, e.offset)
		if !ok {
			return true
		}

		e.dirtype = rec.typeByte()

		switch e.dirtype {
		case EXFAT_DIRRECORD_LABEL:
			// Validate volume label/no-label entry
			if e.validateVolLabelDentry(rec.data) {
				e.populateDirRecordLabel(rec)
			}
		case EXFAT_DIRRECORD_NOLABEL:
			e.entry.name = ""
		case EXFAT_DIRRECORD_BITMAP, EXFAT_DIRRECORD_UPCASE:
			if (e.dirtype == EXFAT_DIRRECORD_BITMAP && e.validateAllocBitmapDentry(rec.data)) ||
				(e.dirtype == EXFAT_DIRRECORD_UPCASE && e.validateUpcaseTableDentry(rec.data)) {
				e.populateRecordBitmapUpcase(rec)
				*entries = append(*entries, e.virtualEntry)
			}
		// These three records carry no content stream. They must be built from a
		// clean Entry rather than by patching the shared virtualEntry, which
		// still holds the cluster and length of whichever $BitMap or $UpCase
		// record was parsed before them.
		case EXFAT_DIRRECORD_VOLUME_GUID:
			e.virtualEntry = Entry{etype: e.dirtype, name: VOLUME_GUID}
			*entries = append(*entries, e.virtualEntry)
		case EXFAT_DIRRECORD_TEXFAT:
			e.virtualEntry = Entry{etype: e.dirtype, name: TEXFAT}
			*entries = append(*entries, e.virtualEntry)
		case EXFAT_DIRRECORD_ACT:
			e.virtualEntry = Entry{etype: e.dirtype, name: ACT}
			*entries = append(*entries, e.virtualEntry)
		default:
			if (e.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_FILEDIR {
				if e.validateFileDentry(rec.data) {
					e.beginEntrySet(rec)
				}
			}
			if ((e.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_STREAM_EXT) &&
				e.secondaryBelongsToSet(rec) {
				if !e.sawStream && e.validateFileStreamDentry(rec.data) {
					e.setChecksum = exfatDirSetChecksumAdd(e.setChecksum, rec.data, false)
					e.populateDirRecordStreamSeen(rec)
					e.expectedNameLen = int(e.entry.nameLen)
					e.sawStream = true
				}
			}
			// Name records are folded into the checksum only while a set is
			// open. A stray 0xC1 outside one - slack, or a directory whose
			// primary record was never parsed - would otherwise corrupt the
			// running checksum and prepend its bytes to the next real name.
			if ((e.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_FILENAME_EXT) &&
				e.secondaryBelongsToSet(rec) {
				if e.validateFileNameDentry(rec.data) {
					e.setChecksum = exfatDirSetChecksumAdd(e.setChecksum, rec.data, false)

					raw := rec.bytes(2, EXFAT_DIRRECORD_SIZE)
					units := utf16leUnitsFromBytes(raw, 15)
					e.nameUnits = append(e.nameUnits, units...)

					if e.remainingSC >= 1 {
						e.remainingSC--

						if e.remainingSC == 0 {
							if e.sawStream {
								e.finishEntrySet(entries)
							} else {
								e.clearParsedEntry()
							}
						}
					}
				}
			}
		}

		e.offset += EXFAT_DIRRECORD_SIZE
	}

	return false
}

func (e *ExFAT) populateDirRecordLabel(rec dirRecordView) {
	count := int(rec.byteAt(1))
	endOffset := 2 + count*2
	if endOffset > len(rec.data) {
		endOffset = len(rec.data)
	}
	e.vbr.volumeLabel = unicodeFromAscii(rec.bytes(2, endOffset), count)
}
func (e *ExFAT) populateRecordBitmapUpcase(rec dirRecordView) {
	entryCluster := rec.le32(20)
	dataLen := rec.le64(24)

	e.virtualEntry.etype = e.dirtype
	e.virtualEntry.dataLen = dataLen
	e.virtualEntry.entryCluster = entryCluster

	// no real dates/times
	e.virtualEntry.modified = 0
	e.virtualEntry.created = 0
	e.virtualEntry.accessed = 0
	e.virtualEntry.modified10ms = 0
	e.virtualEntry.created10ms = 0
	e.virtualEntry.modifiedUtcOffset = 0
	e.virtualEntry.createdUtcOffset = 0
	e.virtualEntry.accessedUtcOffset = 0
	e.virtualEntry.entryAttr = 0
	e.virtualEntry.secondaryCount = 0
	e.virtualEntry.noFatChain = false
	e.virtualEntry.isRegion = false
	e.virtualEntry.regionOffset = 0
	e.virtualEntry.validDataLen = dataLen

	switch e.dirtype {
	case EXFAT_DIRRECORD_BITMAP:
		e.vbr.bitmcapCluster = entryCluster
		e.vbr.bitmapLength = dataLen
		e.virtualEntry.name = BITMAP
		e.vbr.bitmapEntry = e.virtualEntry
	case EXFAT_DIRRECORD_UPCASE:
		e.vbr.upcaseCluster = entryCluster
		e.vbr.upcaseLength = dataLen
		e.virtualEntry.name = UPCASE
	}
}
func (e *ExFAT) populateDirRecordDel(rec dirRecordView) {
	e.entry.etype = e.dirtype
	e.entry.seenRecords = []byte{e.dirtype}
	e.entry.secondaryCount = uint32(rec.byteAt(1))
	e.entry.entryAttr = rec.le16(4)
	e.entry.created = rec.le32(8)
	e.entry.modified = rec.le32(12)
	e.entry.accessed = rec.le32(16)
	e.entry.created10ms = rec.byteAt(20)
	e.entry.modified10ms = rec.byteAt(21)
	// Without these the timestamps above are unanchored wall-clock readings.
	e.entry.createdUtcOffset = rec.byteAt(22)
	e.entry.modifiedUtcOffset = rec.byteAt(23)
	e.entry.accessedUtcOffset = rec.byteAt(24)
	e.remainingSC = int(e.entry.secondaryCount)
	// Both 0x85 (allocated) and 0x05 (deleted) begin a file entry set.
	if (e.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_FILEDIR {
		e.entryState = ENTRY_STATE_85_SEEN
	}
}
func (e *ExFAT) populateDirRecordStreamSeen(rec dirRecordView) {
	e.entry.nameLen = rec.byteAt(3)
	e.entry.readNameLen = 0
	e.entry.entryCluster = rec.le32(20)
	e.entry.dataLen = rec.le64(24)
	// ValidDataLength lives at offset 8 of the stream extension entry, not 24.
	// Reading it from 24 made it a duplicate of DataLength, which hides the
	// allocated-but-never-written tail that slack analysis depends on.
	e.entry.validDataLen = rec.le64(8)

	e.entry.noFatChain = false
	if (rec.byteAt(1) & NOT_FAT_CHAIN_FLAG) != 0 {
		e.entry.noFatChain = true
	}

	e.remainingSC--
}

// createVirtualEntries creates virtual/special entries representing filesystem metadata
// These entries are similar to what SleuthKit's FLS shows for exFAT filesystems
func (e *ExFAT) createVirtualEntries() []Entry {
	var virtualEntries []Entry

	fatBytes := uint64(e.vbr.fatSize) * uint64(e.vbr.sectorSize)

	// $MBR virtual entry - represents the Master Boot Record / VBR.
	// The boot region is 12 sectors of the volume's own sector size, which is
	// not necessarily 512.
	mbrEntry := Entry{
		etype:        0xFF, // Virtual entry type
		name:         MBR,
		dataLen:      uint64(VBR_SIZE) * uint64(e.vbr.sectorSize),
		entryAttr:    ENTRY_ATTR_SYSTEM_MASK | ENTRY_ATTR_HIDDEN_MASK,
		noFatChain:   true,
		isRegion:     true,
		regionOffset: uint64(e.vbr.base),
	}
	mbrEntry.validDataLen = mbrEntry.dataLen
	virtualEntries = append(virtualEntries, mbrEntry)

	// $FAT1 virtual entry - represents the first FAT
	fat1Entry := Entry{
		etype:        0xFF, // Virtual entry type
		name:         FAT1,
		dataLen:      fatBytes,
		validDataLen: fatBytes,
		entryAttr:    ENTRY_ATTR_SYSTEM_MASK | ENTRY_ATTR_HIDDEN_MASK,
		noFatChain:   true,
		isRegion:     true,
		regionOffset: e.vbr.firstFat,
	}
	virtualEntries = append(virtualEntries, fat1Entry)

	// $FAT2 virtual entry - only present on TexFAT volumes, where comparing the
	// two tables is itself an evidentiary signal.
	if e.vbr.numberOfFats == 2 {
		fat2Entry := Entry{
			etype:        0xFF, // Virtual entry type
			name:         FAT2,
			dataLen:      fatBytes,
			validDataLen: fatBytes,
			entryAttr:    ENTRY_ATTR_SYSTEM_MASK | ENTRY_ATTR_HIDDEN_MASK,
			noFatChain:   true,
			isRegion:     true,
			regionOffset: e.vbr.firstFat + fatBytes,
		}
		virtualEntries = append(virtualEntries, fat2Entry)
	}

	// $OrphanFiles virtual directory - represents orphaned/unlinked files
	orphanEntry := Entry{
		etype:      0xFF, // Virtual entry type
		name:       ORPHANFILES,
		dataLen:    0,
		entryAttr:  ENTRY_ATTR_DIR_MASK | ENTRY_ATTR_SYSTEM_MASK | ENTRY_ATTR_HIDDEN_MASK,
		noFatChain: true,
	}
	virtualEntries = append(virtualEntries, orphanEntry)

	return virtualEntries
}
