package libxfat

import (
	"context"
	"errors"
	"fmt"
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

// AllocatedClusters function is experimental, it may not work correctly all the time
// It has been tested to work correctly if used directly after parsing root entries
func (e *ExFAT) AllocatedClusters() (uint32, error) {
	if err := e.ensureBitmapEntry(); err != nil {
		return 0, ErrAllocationBitmapNotFound
	}

	counter := bitmapCounter{}
	err := e.vbr.visitEntryData(e.bitmapStream(), func(_ uint32, chunk []byte) error {
		counter.write(chunk)
		return nil
	})
	if err != nil && !isEOF(err) {
		return 0, err
	}
	allocatedClusters := counter.count()
	return allocatedClusters, nil
}

// FreeClusters function is experimental, it may not work correctly all the time
// It has been tested to work correctly if used directly after parsing root entries
func (e *ExFAT) FreeClusters() (uint32, error) {
	allocatedClusters, err := e.AllocatedClusters()
	if err != nil {
		return 0, err
	}
	freeClusters := e.vbr.nbClusters - allocatedClusters
	return freeClusters, nil
}

func (e *ExFAT) ClusterSize() uint64 {
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
			ErrTruncatedChain, located, file.Size(), entry.Name())
	}
	return nil
}

// ExtractAllFiles writes every file in the volume's live directory tree to dstdir,
// reproducing the directory structure beneath it.
//
// It walks the tree once and streams each file straight from its located extents, so
// nothing is buffered whole and no entry is read twice. Deleted entries are not
// extracted: their clusters may already belong to another file, and writing them out
// as though they were the named file's content would be a fabrication. Use
// OpenEntry, or ExtractEntryContent, on a specific deleted entry when that is what
// you want, and read FragmentResult's flags to see what the bytes are worth.
//
// A file whose chain is broken or truncated is written as far as it could be located
// and then reported: the first such failure is returned after the walk completes, so
// one damaged file does not cost the rest of the volume. Everything extractable is
// extracted whatever this returns.
//
// This prints nothing. It used to write every entry it touched to stdout and "Done!"
// at the end, which made it unusable from anything that had its own output.
func (e *ExFAT) ExtractAllFiles(ctx context.Context, dstdir string) error {
	var firstFailure error

	err := e.Walk(ctx, func(path string, _ uint32, entry Entry) error {
		target, ok := extractionTarget(dstdir, path)
		if !ok {
			// A name the volume recorded resolved outside dstdir. Skipping it is
			// not a silent loss: the entry is still reported by Walk, and
			// ExtractEntryContent will write it wherever the caller chooses.
			if firstFailure == nil {
				firstFailure = fmt.Errorf("%w: %q resolves outside the destination", ErrOutOfBounds, path)
			}
			return nil
		}

		switch {
		case entry.IsDir():
			return os.MkdirAll(target, 0o755)
		case !entry.IsValid(), !entry.IsInUse():
			return nil
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := e.ExtractEntryContent(entry, target); err != nil && firstFailure == nil {
			firstFailure = fmt.Errorf("%s: %w", path, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return firstFailure
}

// extractionTarget maps a walk path onto a path beneath dstdir, and reports
// whether it stayed there.
//
// A name on an exFAT volume is whatever the records say it is. On a hostile or
// merely damaged image that can be "..", or a string of them, and joining such a
// name onto an output directory walks back out of it - so an extraction of an image
// could write anywhere the process can write. The names are the evidence; refusing
// to follow them out of the destination is the library's job.
//
// The leading separator is stripped first because a walk path is always absolute
// and slash-separated, and joining an absolute path is not what is wanted here.
func extractionTarget(dstdir, walkPath string) (string, bool) {
	relative := filepath.Clean(filepath.FromSlash(strings.Trim(walkPath, "/")))
	if relative == "." || filepath.IsAbs(relative) {
		return "", false
	}

	target := filepath.Join(dstdir, relative)

	// Clean has already collapsed any interior "..", so anything still climbing
	// out shows up here as a relative path that starts with one.
	back, err := filepath.Rel(dstdir, target)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", false
	}
	return target, true
}

// ContiguousFilePaths walks the tree below entries and returns the
// indexable ones with their full paths composed, prefixed by path.
//
// Both decisions it makes about an entry - whether to index it, and whether to
// descend into it - are taken before the name is rewritten. The synthetic
// entries identify themselves by name, so "$MBR" turned into "/$MBR" stops
// answering to IsVirtualEntry and reads as an ordinary invalid entry: composing
// the path first silently dropped $MBR and $FAT1 from the results, which is why
// this returned two fewer entries than ContiguousFiles on the same volume.
func (e *ExFAT) ContiguousFilePaths(entries []Entry, path string) ([]Entry, error) {
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
		indexable := entry.IsContiguousFile()

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

		tempRet, err := e.ContiguousFilePaths(subentries, entry.name+"/")
		if err != nil {
			return nil, err
		}
		retentries = append(retentries, tempRet...)
	}

	return retentries, nil
}

// limit - 2,14,74,83,646 entries
func (e *ExFAT) ContiguousFiles(rootEntries []Entry) ([]Entry, error) {
	return e.AllEntries(rootEntries, true)
}

// limit - 2,14,74,83,646 entries
func (e *ExFAT) AllEntries(rootEntries []Entry, indexable ...bool) ([]Entry, error) {
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
			if flag && !subEntry.IsContiguousFile() {
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
	if err != nil && !isEOF(err) {
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
//
// It reads every unallocated cluster on the volume and cannot be interrupted; on
// a large mostly-empty image that is a long time to hold a caller. Use
// RecoverDeletedEntriesContext to be able to stop.
func (e *ExFAT) RecoverDeletedEntries() ([]Entry, error) {
	return e.recoverDeleted(&scanProgress{ctx: context.Background()})
}

// RecoverDeletedEntriesContext is RecoverDeletedEntries, stoppable.
//
// Cancelling ctx abandons the sweep and returns ctx.Err(); the entries carved so
// far are discarded rather than returned, because a partial sweep of a volume's
// free space is not a partial answer to "what was deleted here" - it is an
// unstated subset of it, and a caller cannot tell which. Walk with
// IncludeRecovered reports each carving as it is found, for callers that want the
// partial result.
func (e *ExFAT) RecoverDeletedEntriesContext(ctx context.Context) ([]Entry, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	return e.recoverDeleted(&scanProgress{ctx: ctx})
}

func (e *ExFAT) recoverDeleted(prog *scanProgress) ([]Entry, error) {
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
		if err := prog.check(); err != nil {
			return nil, err
		}

		buf := state.ensureBuf(&e.vbr)
		if err := e.vbr.readClusterInto(cluster, buf); err != nil {
			return nil, err
		}
		parser.resetDirParser()
		deleted = append(deleted, parser.parseDeletedDirEntries(cluster, buf)...)
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
	if err != nil && !isEOF(err) {
		return nil, err
	}

	return unallocated, nil
}

func (e *ExFAT) CountClusters(entry Entry) (int, error) {
	return e.vbr.countClusters(entry)
}

// ClusterList method returns a list of all the clusters in a file
// end index of the last byte in the last cluster of the file
func (e *ExFAT) ClusterList(entry Entry) ([]uint32, uint64, error) {
	return e.vbr.getClusterList(entry)
}

// ClusterOffset is the absolute image offset of the first byte of cluster.
//
// It refuses a cluster number the volume cannot address rather than computing an
// offset from it. exFAT has no cluster 0 or 1, and the arithmetic for cluster 0
// yields the start of the cluster heap minus two clusters - an offset that lands in
// the FAT, or before the start of the volume, and that a caller receives looking
// exactly like an answer. Producing offsets that refer to nothing is the failure
// mode this library exists to avoid.
//
// The offset includes Base, so it indexes the image the volume was opened over.
func (e *ExFAT) ClusterOffset(cluster uint32) (uint64, error) {
	if !e.vbr.isValidCluster(cluster) {
		return 0, fmt.Errorf("%w: %d", ErrInvalidCluster, cluster)
	}
	return e.vbr.getClusterOffset(cluster), nil
}

func (e *ExFAT) readDirEntries(entry Entry) ([]Entry, error) {
	var entries []Entry
	done := false
	parser := newDirParser(&e.vbr, e.optimistic, e.rejectChecksumMismatch)
	parser.inDirectory(entry.entryCluster)
	err := e.vbr.visitEntryData(entry, func(cluster uint32, chunk []byte) error {
		if done {
			return nil
		}
		if parser.parseDirChunk(cluster, chunk, &entries) {
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
	parser.inDirectory(e.vbr.rootDirCluster)
	err := e.vbr.visitFatChain(e.vbr.rootDirCluster, func(cluster uint32, chunk []byte) error {
		if done {
			return nil
		}
		if parser.parseDirChunk(cluster, chunk, &entries) {
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

	fatBytes := e.vbr.fatBytes()

	// $MBR virtual entry - represents the Master Boot Record / VBR.
	// The boot region is 12 sectors of the volume's own sector size, which is
	// not necessarily 512.
	mbrEntry := Entry{
		etype:          0xFF, // Virtual entry type
		name:           MBR,
		dataLen:        uint64(VBR_SIZE) * uint64(e.vbr.sectorSize),
		entryAttr:      ENTRY_ATTR_SYSTEM_MASK | ENTRY_ATTR_HIDDEN_MASK,
		secondaryFlags: ALLOCATION_POSSIBLE_FLAG | NOT_FAT_CHAIN_FLAG,
		isRegion:       true,
		regionOffset:   uint64(e.vbr.base),
	}
	mbrEntry.validDataLen = mbrEntry.dataLen
	virtualEntries = append(virtualEntries, mbrEntry)

	// $FAT1 virtual entry - represents the first FAT
	fat1Entry := Entry{
		etype:          0xFF, // Virtual entry type
		name:           FAT1,
		dataLen:        fatBytes,
		validDataLen:   fatBytes,
		entryAttr:      ENTRY_ATTR_SYSTEM_MASK | ENTRY_ATTR_HIDDEN_MASK,
		secondaryFlags: ALLOCATION_POSSIBLE_FLAG | NOT_FAT_CHAIN_FLAG,
		isRegion:       true,
		regionOffset:   e.vbr.firstFat,
	}
	virtualEntries = append(virtualEntries, fat1Entry)

	// $FAT2 virtual entry - only present on TexFAT volumes, where comparing the
	// two tables is itself an evidentiary signal.
	if e.vbr.numberOfFats == 2 {
		fat2Entry := Entry{
			etype:          0xFF, // Virtual entry type
			name:           FAT2,
			dataLen:        fatBytes,
			validDataLen:   fatBytes,
			entryAttr:      ENTRY_ATTR_SYSTEM_MASK | ENTRY_ATTR_HIDDEN_MASK,
			secondaryFlags: ALLOCATION_POSSIBLE_FLAG | NOT_FAT_CHAIN_FLAG,
			isRegion:       true,
			regionOffset:   e.vbr.firstFat + fatBytes,
		}
		virtualEntries = append(virtualEntries, fat2Entry)
	}

	// $OrphanFiles virtual directory - represents orphaned/unlinked files
	orphanEntry := Entry{
		etype:          0xFF, // Virtual entry type
		name:           ORPHANFILES,
		dataLen:        0,
		entryAttr:      ENTRY_ATTR_DIR_MASK | ENTRY_ATTR_SYSTEM_MASK | ENTRY_ATTR_HIDDEN_MASK,
		secondaryFlags: ALLOCATION_POSSIBLE_FLAG | NOT_FAT_CHAIN_FLAG,
	}
	virtualEntries = append(virtualEntries, orphanEntry)

	return virtualEntries
}
