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
	return e.volumeMetaKnown()
}

// ensureBitmapEntry locates the allocation bitmap, reading the root directory if
// that has not happened yet.
//
// ReadRootDir is deliberately called with no lock held: it publishes what it finds
// under metaMu, so calling it from inside that lock would deadlock. The state is
// re-checked afterwards because another goroutine may have published in between,
// which is fine - both would have found the same entry.
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

// GetAllocatedClusters function is experimental, it may not work correctly all the time
// It has been tested to work correctly if used directly after parsing root entries
func (e *ExFAT) GetAllocatedClusters() (uint32, error) {
	if err := e.ensureBitmapEntry(); err != nil {
		return 0, ErrAllocationBitmapNotFound
	}

	counter := bitmapCounter{}
	err := e.vbr.visitEntryData(e.bitmapStream(), func(_ uint32, chunk []byte) error {
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
	file, err := e.OpenEntry(entry)
	if err != nil {
		return err
	}

	// Created before the zero-length check, so that an entry with no content
	// still yields an empty file rather than leaving the destination absent.
	dstfile, err := os.Create(dstpath)
	if err != nil {
		return err
	}
	defer dstfile.Close()

	if entry.dataLen == 0 {
		return nil
	}

	// One io.Copy over the entry's extents, in place of the three near-identical
	// region, contiguous and chained extractors this used to dispatch between.
	// A region is now simply a result with one range, which is what made them
	// collapse.
	if _, err := file.WriteTo(dstfile); err != nil {
		return err
	}

	// A short chain is reported, not silently written as a shorter file. The
	// bytes that were recovered are already on disk; the error says they are
	// not the whole of what the entry claims.
	if located, err := file.Located(); err == nil && located < file.Size() {
		return fmt.Errorf("%w: %d of %d bytes recoverable for %q",
			ErrTruncatedChain, located, file.Size(), entry.GetName())
	}
	return nil
}

func (e *ExFAT) ExtractAllFiles(rootEntries []Entry, dstdir string) error {
	err := e.getAllEntriesInfo(rootEntries, "/", dstdir, false, false, true)
	if err != nil {
		return err
	}

	fmt.Println("Done!")
	return nil
}

// GetFullPathIndexableEntries walks the tree below entries and returns the
// indexable ones with their full paths composed, prefixed by path.
//
// Both decisions it makes about an entry - whether to index it, and whether to
// descend into it - are taken before the name is rewritten. The synthetic
// entries identify themselves by name, so "$MBR" turned into "/$MBR" stops
// answering to IsVirtualEntry and reads as an ordinary invalid entry: composing
// the path first silently dropped $MBR and $FAT1 from the results, which is why
// this returned two fewer entries than GetIndexableEntries on the same volume.
func (e *ExFAT) GetFullPathIndexableEntries(entries []Entry, path string) ([]Entry, error) {
	// Most entries on a volume are indexable, so this level is the best estimate
	// of the result available before the walk. Seeding from it turns the dozen
	// doubling reallocations of a large directory into one, and costs at worst a
	// slice the size of the input the caller already holds. Left nil for an
	// empty level so the zero case still returns nil, as it always has.
	var retentries []Entry
	if len(entries) > 0 {
		retentries = make([]Entry, 0, len(entries))
	}

	for _, entry := range entries {
		indexable := entry.IsIndexable()

		subentries, err := e.ReadDir(entry)
		if err != nil {
			return nil, err
		}

		entry.name = path + entry.name

		if indexable {
			retentries = append(retentries, entry)
		}

		// Composing the child prefix costs a string, so it is built only when
		// there are children to hand it to. Most entries on a volume are files,
		// and every one of them was paying for a prefix nothing ever read.
		if len(subentries) == 0 {
			continue
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

func (e *ExFAT) processEntry(entry Entry, path, dstdir string, extract, long, simple bool) error {
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
	subEntries := rootEntries

	if len(indexable) > 0 {
		flag = indexable[0]
	}

	// The first level is a lower bound on the result - every level below adds to
	// it - so seeding from it never overshoots by more than what the caller is
	// already holding, and removes the reallocations that dominate the bytes
	// this walk churns on a wide directory.
	var allEntries []Entry
	if len(rootEntries) > 0 {
		allEntries = make([]Entry, 0, len(rootEntries))
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

	// One buffer for the whole scan rather than one per cluster. This loop runs
	// over every unallocated cluster on the volume, so allocating here meant a
	// cluster-sized allocation per free cluster - on a mostly-empty volume, the
	// bulk of what this call costs.
	//
	// Reuse is safe because nothing in an Entry points back at the cluster
	// data: the names are freshly allocated strings and every other field is a
	// scalar copied out of the record.
	state := e.vbr.acquireVisit()
	defer e.vbr.releaseVisit(state)

	// One parser for the whole scan. Its outputs are discarded: a $BitMap or
	// $UpCase record carved out of an unallocated cluster describes a volume
	// state that no longer holds, and believing it would repoint the very
	// structures this scan is driven by.
	parser := newDirParser(&e.vbr, e.optimistic, e.rejectChecksumMismatch)

	for _, cluster := range unallocated {
		buf := state.ensureBuf(&e.vbr)
		if err := e.vbr.readClusterInto(cluster, buf); err != nil {
			return nil, err
		}
		parser.resetDirParser()
		deleted = append(deleted, parser.parseDeletedDirEntries(buf)...)
	}

	return deleted, nil
}

func (e *ExFAT) getUnallocatedClusters() ([]uint32, error) {
	if err := e.ensureBitmapEntry(); err != nil {
		return nil, err
	}

	var unallocated []uint32
	clusterIndex := uint32(0)
	err := e.vbr.visitEntryData(e.bitmapStream(), func(_ uint32, chunk []byte) error {
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

func (e *ExFAT) readDirEntries(entry Entry) ([]Entry, error) {
	var entries []Entry
	done := false
	parser := newDirParser(&e.vbr, e.optimistic, e.rejectChecksumMismatch)
	err := e.vbr.visitEntryData(entry, func(_ uint32, chunk []byte) error {
		if done {
			return nil
		}
		if parser.parseDirChunk(chunk, &entries) {
			done = true
		}
		return nil
	})
	// parser.out is deliberately dropped. The volume label and the $BitMap and
	// $UpCase streams are root-directory records; a record claiming to be one of
	// them in a subdirectory is malformed, and acting on it would let any
	// directory on the volume redefine the volume.
	return entries, err
}

func (e *ExFAT) readRootDirEntries() ([]Entry, error) {
	var entries []Entry
	done := false
	parser := newDirParser(&e.vbr, e.optimistic, e.rejectChecksumMismatch)
	err := e.vbr.visitFatChain(e.vbr.rootDirCluster, func(_ uint32, chunk []byte) error {
		if done {
			return nil
		}
		if parser.parseDirChunk(chunk, &entries) {
			done = true
		}
		return nil
	})
	// The root directory is the only place the volume's own records legitimately
	// live, so it is the only parse whose outputs are published.
	e.publish(parser.out)
	return entries, err
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
