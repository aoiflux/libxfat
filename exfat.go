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
				e.setChecksum = exfatDirSetChecksumAdd(0, rec.data, true)
				e.populateDirRecordDel(rec)
				e.expectedSC = int(e.entry.secondaryCount)
				e.expectedChecksum = uint16(rec.byteAt(2)) | (uint16(rec.byteAt(3)) << 8)
			}
			continue
		}

		if (e.dirtype&0x7f) == EXFAT_DIRRECORD_DEL_STREAM_EXT && e.entryState == ENTRY_STATE_85_SEEN {
			if e.validateFileStreamDentry(rec.data) {
				e.setChecksum = exfatDirSetChecksumAdd(e.setChecksum, rec.data, false)
				e.populateDirRecordStreamSeen(rec)
				e.expectedNameLen = int(e.entry.nameLen)
			}
			continue
		}

		if (e.dirtype&0x7f) == EXFAT_DIRRECORD_DEL_FILENAME_EXT && e.entryState == ENTRY_STATE_85_SEEN {
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

			if e.expectedNameLen > 0 && len(e.nameUnits) > e.expectedNameLen {
				e.nameUnits = e.nameUnits[:e.expectedNameLen]
			}
			if e.optimistic || e.expectedChecksum == e.setChecksum {
				e.entry.name = utf16UnitsToString(e.nameUnits)
			}
			if e.entry.IsDeleted() {
				e.entry.name += DELETED
			}
			entries = append(entries, e.entry)
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
		case EXFAT_DIRRECORD_VOLUME_GUID:
			e.virtualEntry.etype = e.dirtype
			e.virtualEntry.name = VOLUME_GUID
			e.virtualEntry.entryAttr = 0
			*entries = append(*entries, e.virtualEntry)
		case EXFAT_DIRRECORD_TEXFAT:
			e.virtualEntry.etype = e.dirtype
			e.virtualEntry.name = TEXFAT
			e.virtualEntry.entryAttr = 0
			*entries = append(*entries, e.virtualEntry)
		case EXFAT_DIRRECORD_ACT:
			e.virtualEntry.etype = e.dirtype
			e.virtualEntry.name = ACT
			e.virtualEntry.entryAttr = 0
			*entries = append(*entries, e.virtualEntry)
		default:
			if (e.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_FILEDIR {
				if e.validateFileDentry(rec.data) {
					e.setChecksum = exfatDirSetChecksumAdd(0, rec.data, true)
					e.populateDirRecordDel(rec)
					e.expectedSC = int(e.entry.secondaryCount)
					b0 := uint16(rec.byteAt(2))
					b1 := uint16(rec.byteAt(3))
					e.expectedChecksum = b0 | (b1 << 8)
					e.expectedNameLen = 0
					e.nameUnits = nil
				}
			}
			if ((e.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_STREAM_EXT) &&
				(e.entryState == ENTRY_STATE_85_SEEN) {
				if e.validateFileStreamDentry(rec.data) {
					e.setChecksum = exfatDirSetChecksumAdd(e.setChecksum, rec.data, false)
					e.populateDirRecordStreamSeen(rec)
					e.expectedNameLen = int(e.entry.nameLen)
				}
			}
			if (e.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_FILENAME_EXT {
				if e.validateFileNameDentry(rec.data) {
					e.setChecksum = exfatDirSetChecksumAdd(e.setChecksum, rec.data, false)

					raw := rec.bytes(2, EXFAT_DIRRECORD_SIZE)
					units := utf16leUnitsFromBytes(raw, 15)
					e.nameUnits = append(e.nameUnits, units...)

					if (e.entryState == ENTRY_STATE_85_SEEN) && (e.remainingSC >= 1) {
						e.remainingSC--

						if e.remainingSC == 0 {
							if e.expectedNameLen > 0 && len(e.nameUnits) > e.expectedNameLen {
								e.nameUnits = e.nameUnits[:e.expectedNameLen]
							}
							checksumOK := e.expectedChecksum == e.setChecksum
							if e.optimistic || checksumOK {
								e.entry.name = utf16UnitsToString(e.nameUnits)
							} else {
								e.entry.name = ""
							}
							if e.entry.IsDeleted() {
								e.entry.name += DELETED
							}

							*entries = append(*entries, e.entry)
							e.entry = Entry{}
							e.entryState = ENTRY_STATE_LAST_C1_SEEN
							e.resetSetAssembly()
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
	e.entry.validDataLen = rec.le64(24)

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

	// $MBR virtual entry - represents the Master Boot Record / VBR
	mbrEntry := Entry{
		etype:      0xFF, // Virtual entry type
		name:       MBR,
		dataLen:    uint64(VBR_SIZE * SECTOR_SIZE),
		entryAttr:  ENTRY_ATTR_SYSTEM_MASK | ENTRY_ATTR_HIDDEN_MASK,
		noFatChain: true,
	}
	virtualEntries = append(virtualEntries, mbrEntry)

	// $FAT1 virtual entry - represents the first FAT
	fat1Entry := Entry{
		etype:      0xFF, // Virtual entry type
		name:       FAT1,
		dataLen:    uint64(e.vbr.fatSize) * uint64(e.vbr.sectorSize),
		entryAttr:  ENTRY_ATTR_SYSTEM_MASK | ENTRY_ATTR_HIDDEN_MASK,
		noFatChain: true,
	}
	virtualEntries = append(virtualEntries, fat1Entry)

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
