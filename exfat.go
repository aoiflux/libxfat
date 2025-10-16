package libxfat

import (
	"fmt"
	"path/filepath"
)

// hasRangeForClusterData reports whether e.clusterdata has at least
// length bytes starting from start. It avoids panics when parsing
// partially available cluster buffers.
func (e *ExFAT) hasRangeForClusterData(start, length int) bool {
	if e.clusterdata == nil {
		return false
	}
	if start < 0 || length < 0 {
		return false
	}
	return start+length <= len(e.clusterdata)
}

// GetAllocatedClusters function is experimental, it may not work correctly all the time
// It has been tested to work correctly if used directly after parsing root entries
func (e *ExFAT) GetAllocatedClusters() (uint32, error) {
	content, err := e.vbr.readContent(e.vbr.bitmapEntry)
	if err != nil && err.Error() != EOF {
		return 0, err
	}
	allocatedClusters := countBitmap(content)
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

func (e *ExFAT) ExtractEntryContent(entry Entry, dstpath string) error {
	if entry.IsInvalid() {
		return ErrInvalidEntry
	}
	if entry.IsDeleted() {
		return ErrDeletedEntry
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
		e.processEntry(entry, path, dstdir, extract, long, simple)

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
		if entry.IsValid() && entry.IsFile() && entry.IsIndexed() {
			dstpath := filepath.Join(dstdir, entry.name)
			err := e.ExtractEntryContent(entry, dstpath)
			if err != nil {
				return err
			}
		}

		return nil
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
	content, err := e.vbr.readContent(entry)
	if err != nil && err.Error() != EOF {
		return nil, err
	}
	return e.parseDir(content), err
}

func (e *ExFAT) ReadRootDir() ([]Entry, error) {
	clusterdata, err := e.vbr.readClustersFat(e.vbr.rootDirCluster)
	if err != nil {
		return nil, err
	}
	entries := e.parseDir(clusterdata)
	return entries, nil
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

func (e *ExFAT) parseDir(clusterdata []byte) []Entry {
	var entries []Entry
	e.initEntryState(clusterdata, 0, 0, ENTRY_STATE_START)
	// no string assembly here; we accumulate UTF-16 units instead
	// reset per-set parsing state
	e.setChecksum = 0
	e.expectedChecksum = 0
	e.expectedSC = 0
	e.expectedNameLen = 0
	e.nameUnits = nil

	for (e.offset < len(clusterdata)) && clusterdata[e.offset] != 0 {
		// Ensure a full directory record is available before parsing it.
		if e.offset+EXFAT_DIRRECORD_SIZE > len(clusterdata) {
			// Not enough bytes remain for a complete record; stop parsing.
			break
		}

		e.dirtype = clusterdata[e.offset]

		switch e.dirtype {
		case EXFAT_DIRRECORD_LABEL:
			// Validate volume label/no-label entry
			if e.validateVolLabelDentry(clusterdata[e.offset : e.offset+EXFAT_DIRRECORD_SIZE]) {
				e.populateDirRecordLabel()
			}
		case EXFAT_DIRRECORD_NOLABEL:
			e.entry.name = ""
		case EXFAT_DIRRECORD_BITMAP, EXFAT_DIRRECORD_UPCASE:
			rec := clusterdata[e.offset : e.offset+EXFAT_DIRRECORD_SIZE]
			if (e.dirtype == EXFAT_DIRRECORD_BITMAP && e.validateAllocBitmapDentry(rec)) ||
				(e.dirtype == EXFAT_DIRRECORD_UPCASE && e.validateUpcaseTableDentry(rec)) {
				e.populateRecordBitmapUpcase()
			}
		case EXFAT_DIRRECORD_VOLUME_GUID:
			e.entry.name = VOLUME
		default:
			// 0x85
			if (e.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_FILEDIR {
				rec := clusterdata[e.offset : e.offset+EXFAT_DIRRECORD_SIZE]
				if e.validateFileDentry(rec) {
					// Update checksum with this record treating bytes 2..3 as zero (file dir entry)
					e.setChecksum = exfatDirSetChecksumAdd(0, rec, true)
					e.populateDirRecordDel()
					e.expectedSC = int(e.entry.secondaryCount)
					// Capture expected checksum from the record (bytes 2..3 LE)
					b0 := uint16(rec[2])
					b1 := uint16(rec[3])
					e.expectedChecksum = b0 | (b1 << 8)
					e.expectedNameLen = 0
					e.nameUnits = nil
				}
			}
			// 0xc0
			if ((e.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_STREAM_EXT) &&
				(e.entryState == ENTRY_STATE_85_SEEN) {
				rec := clusterdata[e.offset : e.offset+EXFAT_DIRRECORD_SIZE]
				if e.validateFileStreamDentry(rec) {
					// Add stream entry to checksum
					e.setChecksum = exfatDirSetChecksumAdd(e.setChecksum, rec, false)
					e.populateDirRecordStreamSeen()
					e.expectedNameLen = int(e.entry.nameLen)
				}
			}
			// 0xc1
			if (e.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_FILENAME_EXT {
				rec := clusterdata[e.offset : e.offset+EXFAT_DIRRECORD_SIZE]
				if e.validateFileNameDentry(rec) {
					// Include this filename entry into checksum and assemble name units.
					e.setChecksum = exfatDirSetChecksumAdd(e.setChecksum, rec, false)

					// Each filename entry contains up to 15 UTF-16 chars starting at byte offset 2
					raw := rec[2:EXFAT_DIRRECORD_SIZE]
					units := utf16leUnitsFromBytes(raw, 15)
					e.nameUnits = append(e.nameUnits, units...)

					if (e.entryState == ENTRY_STATE_85_SEEN) && (e.remainingSC >= 1) {
						// Defer UTF-8 conversion until the end to use expectedNameLen
						e.remainingSC--

						if e.remainingSC == 0 {
							// Truncate to expectedNameLen (from stream entry) if provided
							if e.expectedNameLen > 0 && len(e.nameUnits) > e.expectedNameLen {
								e.nameUnits = e.nameUnits[:e.expectedNameLen]
							}
							// Only accept if checksum matches (unless optimistic parsing is enabled)
							checksumOK := (e.expectedChecksum == e.setChecksum)
							if e.optimistic || checksumOK {
								e.entry.name = utf16UnitsToString(e.nameUnits)
							} else {
								// Reject or blank the name if checksum fails
								e.entry.name = ""
							}
							if e.entry.IsDeleted() {
								e.entry.name += DELETED
							}

							entries = append(entries, e.entry)
							// Reset per-set state
							e.entry.name = ""
							e.entryState = ENTRY_STATE_LAST_C1_SEEN
							e.setChecksum = 0
							e.expectedChecksum = 0
							e.expectedSC = 0
							e.expectedNameLen = 0
							e.nameUnits = nil
						}
					}
				}
			}
		}

		e.offset += EXFAT_DIRRECORD_SIZE
	}

	return entries
}

func (e *ExFAT) populateDirRecordLabel() {
	count := int(e.clusterdata[e.offset+1])
	startOffset := e.offset + 2
	// Validate offsets and perform safe slicing.
	if !e.hasRangeForClusterData(startOffset, 0) {
		// Nothing to do; cluster buffer too small for label start.
		e.vbr.volumeLabel = ""
		return
	}

	endOffset := e.offset + 2 + count*2
	if endOffset > len(e.clusterdata) {
		endOffset = len(e.clusterdata)
	}
	if startOffset > endOffset {
		startOffset = endOffset
	}
	label := e.clusterdata[startOffset:endOffset]
	e.vbr.volumeLabel = string(label)
}
func (e *ExFAT) populateRecordBitmapUpcase() {
	// Ensure the fields we will read exist
	if !e.hasRangeForClusterData(e.offset+20, 12) {
		return
	}
	entryCluster := unpackLELong(e.clusterdata[e.offset+20 : e.offset+24])
	dataLen := unpackLELongLong(e.clusterdata[e.offset+24 : e.offset+32])

	e.virtualEntry.etype = e.dirtype
	e.virtualEntry.dataLen = dataLen
	e.virtualEntry.entryCluster = entryCluster

	// no real dates/times
	e.virtualEntry.modified = 0
	e.virtualEntry.created = 0
	e.virtualEntry.accessed = 0
	e.virtualEntry.modified10ms = 0
	e.virtualEntry.created10ms = 0
	e.virtualEntry.entryAttr = 0
	e.virtualEntry.secondaryCount = 0
	e.virtualEntry.noFatChain = false

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
func (e *ExFAT) populateDirRecordDel() {
	// Make sure we have at least the minimum bytes to parse the fields we need.
	if !e.hasRangeForClusterData(e.offset, 22) {
		// Buffer too small; treat as partial and return.
		return
	}

	e.entry.etype = e.dirtype
	e.entry.seenRecords = []byte{e.dirtype}
	e.entry.secondaryCount = uint32(e.clusterdata[e.offset+1])
	e.entry.entryAttr = unpackLEShort(e.clusterdata[e.offset+4 : e.offset+6])
	e.entry.created = unpackLELong(e.clusterdata[e.offset+8 : e.offset+12])
	e.entry.modified = unpackLELong(e.clusterdata[e.offset+12 : e.offset+16])
	e.entry.accessed = unpackLELong(e.clusterdata[e.offset+16 : e.offset+20])
	e.entry.created10ms = e.clusterdata[e.offset+20]
	e.entry.modified10ms = e.clusterdata[e.offset+21]
	e.remainingSC = int(e.entry.secondaryCount)
	if e.dirtype == EXFAT_DIRRECORD_FILEDIR {
		e.entryState = ENTRY_STATE_85_SEEN
	}
}
func (e *ExFAT) populateDirRecordStreamSeen() {
	if !e.hasRangeForClusterData(e.offset, EXFAT_DIRRECORD_SIZE) {
		return
	}

	e.entry.nameLen = e.clusterdata[e.offset+3]
	e.entry.readNameLen = 0
	e.entry.entryCluster = unpackLELong(e.clusterdata[e.offset+20 : e.offset+24])
	e.entry.dataLen = unpackLELongLong(e.clusterdata[e.offset+24 : e.offset+32])
	e.entry.validDataLen = unpackLELongLong(e.clusterdata[e.offset+24 : e.offset+32])

	e.entry.noFatChain = false
	if (e.clusterdata[e.offset+1] & NOT_FAT_CHAIN_FLAG) != 0 {
		e.entry.noFatChain = true
	}

	e.remainingSC--
}
