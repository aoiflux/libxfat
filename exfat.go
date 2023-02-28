package libxfat

import (
	"fmt"
	"path/filepath"
)

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
	if entry.IsDir() {
		fmt.Println("Extracting a FOLDER: ", entry.name)
	}
	return e.vbr.extractEntryContent(entry, dstpath)
}

func (e *ExFAT) ExtractAllFiles(rootEntries []Entry, dstdir string) error {
	return e.getAllEntriesInfo(rootEntries, "/", dstdir, false, true)
}

func (e *ExFAT) ShowAllEntriesInfo(rootEntries []Entry, path string, long bool) error {
	return e.getAllEntriesInfo(rootEntries, path, "", long, false)
}

func (e *ExFAT) getAllEntriesInfo(entries []Entry, path, dstdir string, long bool, extract bool) error {
	var entryString string

	for _, entry := range entries {
		if extract {
			if entry.IsValid() && entry.IsFile() && entry.IsIndexed() {
				dstpath := filepath.Join(dstdir, entry.name)
				err := e.ExtractEntryContent(entry, dstpath)
				if err != nil {
					return err
				}
				fmt.Println("Extracted: ", entry.name)
			}
		} else {
			entryString = getDirEntry(entry, path, long)
			fmt.Println(entryString)
		}

		subentries, err := e.ReadDir(entry)
		if err != nil {
			return err
		}

		err = e.getAllEntriesInfo(subentries, path+entry.name+"/", dstdir, long, extract)
		if err != nil {
			return err
		}
	}

	return nil
}

func (e *ExFAT) GetFiles(rootEntries []Entry) ([]Entry, error) {
	var err error
	allEntries := rootEntries
	subEntries := rootEntries
	for {
		subEntries, err = e.ReadDirs(subEntries)
		if err != nil {
			return nil, err
		}
		if subEntries == nil || len(subEntries) < 1 {
			break
		}
		allEntries = append(allEntries, subEntries...)
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
	var namepart string

	for (e.offset < len(clusterdata)) && clusterdata[e.offset] != 0 {
		e.dirtype = clusterdata[e.offset]

		switch e.dirtype {
		case EXFAT_DIRRECORD_LABEL:
			e.populateDirRecordLabel()
		case EXFAT_DIRRECORD_NOLABEL:
			// todo: do something with this type of entry
		case EXFAT_DIRRECORD_BITMAP, EXFAT_DIRRECORD_UPCASE:
			e.populateRecordBitmapUpcase()
		case EXFAT_DIRRECORD_VOLUME_GUID:
			e.entry.name = VOLUME
		default:
			// 0x85
			if (e.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_FILEDIR {
				e.populateDirRecordDel()
			}
			// 0xc0
			if ((e.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_STREAM_EXT) &&
				(e.entryState == ENTRY_STATE_85_SEEN) {
				e.populateDirRecordStreamSeen()
			}
			// 0xc1
			if (e.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_FILENAME_EXT {
				namepart = unicodeFromAscii(clusterdata[e.offset+2:e.offset+EXFAT_DIRRECORD_SIZE], 15)

				if (e.entryState == ENTRY_STATE_85_SEEN) && (e.remainingSC >= 1) {
					e.entry.name += namepart
					e.remainingSC--

					if e.remainingSC == 0 {
						if e.entry.IsDeleted() {
							e.entry.name += DELETED
						}

						entries = append(entries, e.entry)
						e.entry.name = ""
						e.entryState = ENTRY_STATE_LAST_C1_SEEN
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
	if startOffset >= len(e.clusterdata) {
		startOffset = len(e.clusterdata) - 1
	}

	endOffset := e.offset + count*2
	if endOffset >= len(e.clusterdata) {
		endOffset = len(e.clusterdata) - 1
	}

	if startOffset > endOffset {
		startOffset = endOffset
	}
	label := e.clusterdata[startOffset:endOffset]
	e.vbr.volumeLabel = string(label)
}
func (e *ExFAT) populateRecordBitmapUpcase() {
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
	e.entry.etype = e.dirtype
	e.entry.seenRecords = []byte{e.dirtype}
	e.entry.secondaryCount = uint32(e.clusterdata[e.offset+1])
	e.entry.entryAttr = unpackLEShort(e.clusterdata[e.offset+4 : e.offset+6])
	e.entry.created = unpackLELong(e.clusterdata[e.offset+8 : e.offset+12])
	e.entry.modified = unpackLELong(e.clusterdata[e.offset+12 : e.offset+16])
	e.entry.accessed = unpackLELong(e.clusterdata[e.offset+16 : e.offset+20])
	if len(e.clusterdata)-1 >= e.offset+20 {
		e.entry.created10ms = e.clusterdata[e.offset+20]
	}
	if len(e.clusterdata)-1 >= e.offset+21 {
		e.entry.modified10ms = e.clusterdata[e.offset+21]
	}
	e.remainingSC = int(e.entry.secondaryCount)
	if e.dirtype == EXFAT_DIRRECORD_FILEDIR {
		e.entryState = ENTRY_STATE_85_SEEN
	}
}
func (e *ExFAT) populateDirRecordStreamSeen() {
	e.entry.nameLen = e.clusterdata[e.offset+3]
	e.entry.readNameLen = 0
	e.entry.entryCluster = unpackLELong(e.clusterdata[e.offset+20 : e.offset+24])
	e.entry.dataLen = unpackLELongLong(e.clusterdata[e.offset+24 : e.offset+32])
	e.entry.validDataLen = unpackLELongLong(e.clusterdata[e.offset+24 : e.offset+32])

	e.entry.noFatChain = false
	if (e.clusterdata[e.offset+1] & NOT_FAT_CHAIN_FLAG) > 1 {
		e.entry.noFatChain = true
	}

	e.remainingSC--
}
