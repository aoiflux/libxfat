package libxfat

import "context"

// maxWalkDepth bounds how deep a walk descends.
//
// The cycle guard already stops a directory being entered twice, so this is the
// backstop for a crafted tree that is deep without repeating, which would
// otherwise grow the stack without limit. The cap is far past anything a real
// volume produces.
const maxWalkDepth = 4096

// RecoveredPath is the path prefix Walk reports carved entries under when
// WalkOptions.IncludeRecovered is set.
//
// It is not a directory on the volume. A carved record was found in a cluster that
// no directory references, so there is no path to compose and nothing to compose it
// from; this stands in so that every entry a walk reports has a path, and so that a
// caller can recognise the ones whose path is the library's invention rather than
// the volume's.
const RecoveredPath = "/" + ORPHANFILES

// WalkOptions tunes a walk.
//
// The zero value is the precision-first configuration: the live, reachable tree
// only. Deleted records and carved entries are evidence a caller must ask for,
// because including them changes what a change-detection pass sees, and a caller
// who did not ask should not silently receive records for files that are not
// there.
//
// This is deliberately not ReadDir's behaviour. ReadDir has always returned
// deleted entries unconditionally, since a directory listing that hid them would
// hide the thing this package exists to show. A walk is a different operation
// with a different default, and the difference is called out here because it is
// the one place the two disagree.
type WalkOptions struct {
	// IncludeDeleted reports entries whose primary record carries the 0x05
	// deleted type rather than 0x85. They are reported at the slot they
	// physically occupy, so a deleted entry appears among its live siblings in
	// disk order rather than appended at the end.
	IncludeDeleted bool

	// DescendDeletedDirectories walks into a deleted directory and reports the
	// records that survive in its clusters. It has no effect unless
	// IncludeDeleted is set, since the directory itself would not be reported.
	//
	// What gets read is exactly what FragmentOffsets locates for the same entry,
	// and for the same reason: a deleted entry's FAT chain has been freed, so
	// there is no chain left to follow. In the usual case that means one cluster,
	// the first, which is the only location the surviving record names. An entry
	// whose stream extension recorded NoFatChain is the exception - the volume
	// stated the whole range was contiguous, and deletion did not erase that
	// statement - so its full declared range is read.
	//
	// The descent is abandoned if the first cluster is allocated again, because
	// the bitmap then says the bytes belong to something else and parsing them as
	// directory records would attribute one file's content to another file's
	// name. exFAT has no "." record to cross-check against, unlike FAT12 and
	// FAT16, so the allocation state is the only guard available.
	DescendDeletedDirectories bool

	// IncludeRecovered appends what RecoverDeletedEntries carves out of
	// unallocated clusters, after the whole reachable tree has been reported.
	// Their paths are rooted at ORPHANFILES, because the directory that named
	// them is gone and there is no path to compose.
	//
	// This costs considerably more than a plain walk: it reads every unallocated
	// cluster on the volume. The two traversals are kept separate rather than
	// shared because they answer different questions - this walk follows names,
	// while the sweep establishes that nothing reaches a cluster.
	//
	// With DescendDeletedDirectories one record can be reported twice: once under
	// the path its deleted parent still names, and once under ORPHANFILES because
	// the sweep found its cluster unreferenced. Those are two genuinely different
	// findings, so neither is suppressed. A caller wanting one row per record can
	// collapse them on the entry-set offset, which is the same for both.
	IncludeRecovered bool

	// MaxDepth is the number of directory levels reported, counting the root's
	// children as the first. A MaxDepth of 1 reports the root's children and
	// descends no further; 2 adds their children, and so on. Zero selects
	// maxWalkDepth, and larger values are clamped to it: the constant is a
	// ceiling on stack growth, not a default to be raised.
	//
	// A directory sitting on the limit is still reported; only its contents are
	// not read. Exceeding the limit is not an error, because a crafted image that
	// nests a million directories should truncate a walk rather than fail it, and
	// because the truncation is visible in the absence of children under a
	// directory the callback was handed.
	MaxDepth int

	// StopOnReadError makes a directory that cannot be read abort the walk and
	// return the underlying error. By default such a directory is reported - its
	// own record is real evidence whether or not what it points at survives - its
	// contents are skipped, and the walk continues, which is the same choice
	// FragmentResult makes when it returns the runs it managed to walk.
	StopOnReadError bool
}

// Walk calls fn for every entry in the volume's directory tree, depth first and
// in disk order.
//
// It is WalkWithOptions with the zero WalkOptions: the live, reachable tree,
// without deleted records or carved entries.
func (e *ExFAT) Walk(ctx context.Context,
	fn func(path string, parentFirstCluster uint32, entry Entry) error,
) error {
	return e.walk(ctx, WalkOptions{}, fn)
}

// WalkWithOptions calls fn for every entry the options select.
//
// # Order
//
// Pre-order and depth first. Within a directory, entries are reported in the
// order their 32-byte records appear in the directory's clusters - disk order,
// not sorted order - because that order is itself evidence: a deleted record's
// position tells you which live records were written around it. A subdirectory is
// reported and then immediately descended into, so its whole subtree precedes its
// next sibling.
//
// # The root
//
// The root directory is not reported. It has no directory record anywhere on the
// volume: no name, no timestamps, no attributes, no parent. Synthesising an Entry
// for it would fabricate a structure that does not exist. The walk begins by
// reading the root and reporting its children.
//
// # The callback's arguments
//
// path is composed for the callback and is never written into the entry. Entry is
// immutable here, so Entry.Name stays the bare name at every depth - unlike
// ContiguousFilePaths, which rewrites the name into the path and in doing
// so loses the name.
//
// A deleted entry's name carries the library's deleted marker, so its path does
// too. Entry.RawName is the name as recorded on the volume, without it.
//
// parentFirstCluster is the first cluster of the directory the entry was read
// from, which for the root's children is the volume's real root directory cluster
// - exFAT addresses its root by cluster, so nothing stands in for it. For
// anything read off the volume it equals Entry.ParentFirstCluster. The synthetic
// entries are the one exception: they are reported as part of the root listing, so
// they arrive with the root's cluster, while Entry.ParentFirstCluster is zero
// because they have no record to have a parent. Together with
// Entry.EntrySlotIndex this argument forms the entry's FileID.
//
// # What is reported
//
// Deleted records and carved entries are opt-in; see WalkOptions. The entries the
// library synthesises for metadata ($MBR, $FAT1, $FAT2, $OrphanFiles) and the
// volume's own streams ($BitMap, $UpCase) are reported, because they name byte
// ranges that a change-detection consumer has to account for, and are never
// descended into.
//
// Nothing is filtered on fragmentation. ContiguousFiles and
// ContiguousFilePaths both exclude any file with a FAT chain, and so omit
// every fragmented file on the volume; a walk that inherited that would be
// useless for the purpose this one exists for.
//
// # Errors and limits
//
// An error returned by fn stops the walk immediately and is returned unchanged;
// there are no SkipDir or SkipAll sentinels. A directory that cannot be read is
// skipped and the walk continues unless StopOnReadError is set; a root that
// cannot be read is always an error, since there is then nothing to walk. A
// directory reachable more than once - which a valid volume cannot produce - is
// reported each time it is named but descended into only once, keyed on its first
// cluster. Depth is capped; see WalkOptions.MaxDepth.
//
// A skipped directory is not otherwise observable: this package has no warning
// sink, so a walk that stepped over an unreadable directory returns the same nil a
// clean one does. To find out why a directory reported no children, call ReadDir
// on it, which returns the underlying error.
//
// Cancelling ctx stops the walk and returns ctx.Err() unchanged. Entries already
// handed to fn are the partial result. A nil ctx or a nil fn is an error.
func (e *ExFAT) WalkWithOptions(ctx context.Context, opts WalkOptions,
	fn func(path string, parentFirstCluster uint32, entry Entry) error,
) error {
	return e.walk(ctx, opts, fn)
}

// treeWalk is the state of one traversal. Both exported shapes go through it, so
// the cycle guard, the depth cap, the pacing of cancellation checks and the order
// entries are reported in cannot drift apart between them.
type treeWalk struct {
	fs   *ExFAT
	opts WalkOptions
	fn   func(string, uint32, Entry) error
	prog *scanProgress
	// seen holds the first cluster of every directory already descended into.
	seen     map[uint32]struct{}
	maxDepth int
}

func (e *ExFAT) walk(ctx context.Context, opts WalkOptions,
	fn func(path string, parentFirstCluster uint32, entry Entry) error,
) error {
	if ctx == nil {
		return ErrNilContext
	}
	if fn == nil {
		return ErrNilCallback
	}

	depth := opts.MaxDepth
	if depth <= 0 || depth > maxWalkDepth {
		depth = maxWalkDepth
	}

	// The root parse is also what publishes the volume label and the $BitMap and
	// $UpCase streams, so it has to happen before anything that depends on them -
	// which IncludeRecovered does.
	root, err := e.ReadRootDir()
	if err != nil {
		// A root that cannot be read leaves nothing to walk, so unlike any other
		// directory this is always fatal.
		return err
	}

	w := &treeWalk{
		fs:       e,
		opts:     opts,
		fn:       fn,
		prog:     &scanProgress{ctx: ctx},
		seen:     make(map[uint32]struct{}),
		maxDepth: depth,
	}
	// exFAT's root is an ordinary cluster chain, so seeding it is both possible
	// and necessary: a directory record pointing back at the root would otherwise
	// send the walk round it again.
	w.seen[e.vbr.rootDirCluster] = struct{}{}

	if err := w.report(root, "/", e.vbr.rootDirCluster, 0); err != nil {
		return err
	}

	if opts.IncludeRecovered {
		return w.recovered()
	}
	return nil
}

// report hands one directory's entries to the callback and descends into the
// subdirectories among them. prefix ends in a separator, so a child's path is the
// prefix and the name with nothing between them.
func (w *treeWalk) report(entries []Entry, prefix string, parent uint32, depth int) error {
	for _, entry := range entries {
		if err := w.prog.check(); err != nil {
			return err
		}
		if entry.IsDeleted() && !w.opts.IncludeDeleted {
			continue
		}

		path := prefix + entry.Name()
		if err := w.fn(path, parent, entry); err != nil {
			return err
		}

		if !entry.IsDir() || entry.IsSpecialFile() {
			continue
		}
		if depth+1 >= w.maxDepth {
			continue
		}
		if !w.fs.vbr.isValidCluster(entry.entryCluster) {
			continue
		}
		if _, done := w.seen[entry.entryCluster]; done {
			continue
		}

		// Marked before the attempt, not after it: a corrupt image can name the
		// same unreadable cluster many times, and one failed read of it is enough.
		w.seen[entry.entryCluster] = struct{}{}

		children, ok, err := w.children(entry)
		if err != nil {
			if w.opts.StopOnReadError {
				return err
			}
			continue
		}
		if !ok {
			continue
		}
		if err := w.report(children, path+"/", entry.entryCluster, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// children reads a directory entry's contents. The boolean is false when the
// entry is a deleted directory that the options or the surviving evidence do not
// permit descending into, which is not an error.
func (w *treeWalk) children(entry Entry) ([]Entry, bool, error) {
	if entry.IsDeleted() {
		return w.deletedChildren(entry)
	}

	entries, err := w.fs.ReadDir(entry)
	// ReadDir tolerates the end of a truncated image and returns what it read
	// alongside the EOF; a directory running off the end of the image is not a
	// reason to lose the records that were there.
	if err != nil && !isEOF(err) {
		return nil, false, err
	}
	return entries, true, nil
}

// deletedChildren reads the records that survive in a deleted directory's
// clusters.
//
// It locates the clusters through the extent API rather than by walking the FAT,
// because a deleted entry's chain has been freed: whatever the FAT now says about
// those clusters describes their next owner, not this directory. That leaves the
// first cluster, which the surviving record names outright, and - for an entry
// that recorded NoFatChain - the rest of the range the volume declared
// contiguous.
//
// Each cluster is checked against the allocation bitmap immediately before it is
// read, and the descent stops at the first one that is in use again. Parsing a
// reallocated cluster would take some later file's content and report it as this
// directory's children.
func (w *treeWalk) deletedChildren(entry Entry) ([]Entry, bool, error) {
	if !w.opts.DescendDeletedDirectories {
		return nil, false, nil
	}

	located, err := w.fs.FragmentOffsetsWithOptions(entry, FragmentOptions{})
	if err != nil {
		return nil, false, err
	}
	if located.FirstClusterReallocated {
		return nil, false, nil
	}

	parser := newDirParser(&w.fs.vbr, w.fs.optimistic, w.fs.rejectChecksumMismatch)
	parser.inDirectory(entry.entryCluster)

	state := w.fs.vbr.acquireVisit()
	defer w.fs.vbr.releaseVisit(state)

	var entries []Entry
	for _, run := range located.Ranges {
		for i := uint32(0); i < run.ClusterCount; i++ {
			cluster := run.StartCluster + i

			// An unreadable bitmap is not evidence that the cluster was reused,
			// so it does not stop the descent; a bitmap that says "in use" is.
			if allocated, bitmapErr := w.fs.IsClusterAllocated(cluster); bitmapErr == nil && allocated {
				return entries, true, nil
			}

			if err := w.prog.check(); err != nil {
				return nil, false, err
			}

			buf := state.ensureBuf(&w.fs.vbr)
			if err := w.fs.vbr.readClusterInto(cluster, buf); err != nil {
				return nil, false, err
			}
			if parser.parseDirChunk(cluster, buf, &entries) {
				return entries, true, nil
			}
		}
	}
	return entries, true, nil
}

// recovered reports the entries carved out of clusters that no path reaches,
// after the whole reachable tree.
func (w *treeWalk) recovered() error {
	carved, err := w.fs.recoverDeleted(w.prog)
	if err != nil {
		return err
	}

	prefix := RecoveredPath + "/"
	for _, entry := range carved {
		if perr := w.prog.check(); perr != nil {
			return perr
		}
		// IncludeDeleted governs the reachable tree only. A carved record is
		// recovery material in its entirety, and filtering these by the deletion
		// marker would drop exactly what the sweep exists to find.
		//
		// The parent cluster is zero because there is none: the directory that
		// listed these is gone, which is what makes them carvings rather than
		// entries. Entry.EntrySetOffset still says where each record is.
		if ferr := w.fn(prefix+entry.Name(), 0, entry); ferr != nil {
			return ferr
		}
	}
	return nil
}
