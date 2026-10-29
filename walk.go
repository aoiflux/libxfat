package libxfat

import (
	"context"
	"errors"
	"fmt"
)

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

// SkipReason names why a walk reported a directory but did not descend into it.
// It is passed to WalkOptions.OnSkip, which is the only way to observe a skip: the
// alternative signal, a directory reported without children, is what an empty
// directory looks like too.
//
// The names and values are those of the sibling libfat, so a consumer walking both
// maps them to its own findings once.
type SkipReason string

const (
	// SkipReadError: the directory's contents could not be read. The underlying
	// error is passed to OnSkip alongside this reason. A live directory ReadDir
	// declines to read - one whose name is blank - arrives here too, with an error
	// wrapping ErrInvalidEntry: ReadDir returns nothing for it and no error, which
	// a walk would otherwise take for an empty directory.
	SkipReadError SkipReason = "read-error"
	// SkipDepthLimit: the directory sits on WalkOptions.MaxDepth, so it is
	// reported but not read.
	SkipDepthLimit SkipReason = "depth-limit"
	// SkipRevisit: the directory's first cluster had already been descended into,
	// or is the root's. A valid volume cannot produce this; it is a cycle or a
	// cross-link, and the record naming it is still reported as evidence.
	SkipRevisit SkipReason = "revisit"
	// SkipInvalidCluster: the first cluster is outside the cluster heap, so there
	// is nothing to read and no read to fail. The parser already refuses a record
	// naming such a cluster while claiming data, so in practice this is a
	// directory record whose DataLength is zero.
	SkipInvalidCluster SkipReason = "invalid-cluster"
	// SkipDeletedUnrecoverable: DescendDeletedDirectories was set, but the deleted
	// directory's first cluster is allocated again, so its bytes now belong to
	// something else. See DescendDeletedDirectories.
	SkipDeletedUnrecoverable SkipReason = "deleted-unrecoverable"
	// SkipPartialRead: the directory was descended into, but only part of it could
	// be read. The children that were read are reported after this, and the error
	// passed alongside says what cut the read short - the end of the image, for a
	// live directory, or for a deleted one a cluster the bitmap says is in use
	// again, as an error wrapping ErrDeletedEntry. libfat has no such reason: a FAT
	// directory is read whole or fails.
	SkipPartialRead SkipReason = "partial-read"
)

// RecoveredScanResult is the free-space sweep's own account of what it covered,
// which the entries it carves do not carry.
type RecoveredScanResult struct {
	// ClustersSwept is the number of unallocated clusters the sweep read.
	ClustersSwept uint32
	// ClustersUnclassified is the number of heap clusters the allocation bitmap
	// could not answer for, because the image ends inside the $BitMap stream. They
	// were neither read nor ruled out.
	ClustersUnclassified uint32
	// Truncated reports that ClustersUnclassified is not zero: the sweep did not
	// cover the whole heap, so there may be records no path reaches that it did
	// not find. The field and its meaning match libfat's OrphanScanResult.
	Truncated bool
}

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
	// FAT16, so the allocation state is the only guard available. A directory
	// abandoned this way is reported to OnSkip as SkipDeletedUnrecoverable.
	//
	// A deleted directory counts as walked, for the cycle guard, only once it has
	// been descended into. Until then its first cluster is not its own to claim,
	// and the likeliest owner is a live directory created after it was removed:
	// the new record is written after the stale one and handed the freed cluster.
	// That live directory is walked whether or not the deleted one came first.
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
	//
	// The sweep can only read the clusters the allocation bitmap marks free, so
	// an image that ends inside the $BitMap stream leaves the rest of the heap
	// unswept. OnRecoveredScan reports that; the carved entries cannot.
	IncludeRecovered bool

	// MaxDepth is the number of directory levels reported, counting the root's
	// children as the first. A MaxDepth of 1 reports the root's children and
	// descends no further; 2 adds their children, and so on. Zero selects
	// maxWalkDepth, and larger values are clamped to it: the constant is a
	// ceiling on stack growth, not a default to be raised.
	//
	// A directory sitting on the limit is still reported; only its contents are
	// not read. Exceeding the limit is not an error, because a crafted image that
	// nests a million directories should truncate a walk rather than fail it.
	// Set OnSkip to learn which directories were truncated this way: the only
	// other signal is the absence of children under a directory the callback was
	// handed, which is indistinguishable from an empty one.
	MaxDepth int

	// StopOnReadError makes a directory that cannot be read abort the walk and
	// return the underlying error. By default such a directory is reported - its
	// own record is real evidence whether or not what it points at survives - its
	// contents are skipped, and the walk continues, which is the same choice
	// FragmentResult makes when it returns the runs it managed to walk. OnSkip
	// reports those directories without aborting anything.
	StopOnReadError bool

	// OnSkip is called for every directory the walk reported but did not descend
	// into, or descended into only in part, with the reason and, for
	// SkipReadError and SkipPartialRead, the error that stopped the read. It is
	// how a caller tells an empty directory from a truncated one, which is
	// otherwise impossible: both are reported without children.
	//
	// It is only ever called for the entry the walk callback was handed
	// immediately before, with the same path, and at most once for each. The
	// conditions are tested in the order SkipDepthLimit, SkipInvalidCluster,
	// SkipRevisit, SkipReadError, SkipDeletedUnrecoverable, SkipPartialRead, and
	// the first that holds is the one reported. A live directory named twice
	// whose first read failed is reported SkipReadError and then SkipRevisit,
	// because the cycle guard is marked before the attempt. SkipPartialRead is
	// reported before the children that were read.
	//
	// Nothing is reported for a file, for the $BitMap, $UpCase and synthetic
	// entries - which name no directory data - for a deleted record
	// IncludeDeleted left out, or for a deleted directory not descended because
	// DescendDeletedDirectories is off: that last is the caller's own choice
	// rather than something the walk decided.
	//
	// With StopOnReadError set, an unreadable directory aborts the walk and
	// OnSkip is not called, because nothing was skipped. The blank-named
	// directory ReadDir declines and a partly read directory are the exceptions:
	// neither has ever been treated as a failure, so neither has ever aborted a
	// walk, and both are reported here whether or not StopOnReadError is set.
	//
	// An error returned by OnSkip stops the walk immediately and is returned
	// unchanged, exactly as the walk callback's own error is.
	OnSkip func(path string, e Entry, reason SkipReason, err error) error

	// OnRecoveredScan is called once with the result of the sweep IncludeRecovered
	// performs, before any carved entry is reported, and only when the sweep
	// completed; a sweep that fails returns its error from the walk instead. It
	// exists because the walk reports the sweep's entries but otherwise discards
	// its provenance: RecoveredScanResult.Truncated says part of the heap was
	// never swept.
	//
	// An error returned from it stops the walk with that error, and no carved
	// entry is reported. The walk does not use the result afterwards, so the
	// callback may keep it.
	OnRecoveredScan func(result *RecoveredScanResult) error
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
// A deleted entry's path is composed from the name the volume recorded, with no
// marker appended to either. Entry.IsDeleted is what says the record is deleted; a
// caller rendering the tree for a person decides how to show that.
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
// Set WalkOptions.OnSkip to learn about every directory the walk declined to
// descend into - unreadable, depth-capped, already walked, naming a cluster
// outside the heap, or a deleted directory whose cluster has been taken again -
// or could read only part of, with the reason and the read error if there was
// one. Without it a skipped directory is not otherwise observable: this package
// has no warning sink, so a walk that stepped over an unreadable directory
// returns the same nil a clean one does, and the only remaining way to find out
// why a directory reported no children is to call ReadDir on it, which returns
// the underlying error.
//
// With IncludeRecovered, WalkOptions.OnRecoveredScan likewise reports what the
// free-space sweep itself could not cover.
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
	// seen holds the first cluster of every directory already descended into,
	// and of every live directory whose read was attempted.
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
			if err := w.skip(path, entry, SkipDepthLimit, nil); err != nil {
				return err
			}
			continue
		}
		if !w.fs.vbr.isValidCluster(entry.entryCluster) {
			if err := w.skip(path, entry, SkipInvalidCluster, nil); err != nil {
				return err
			}
			continue
		}
		if _, done := w.seen[entry.entryCluster]; done {
			if err := w.skip(path, entry, SkipRevisit, nil); err != nil {
				return err
			}
			continue
		}

		// A live directory is marked before the attempt, not after it: a corrupt
		// image can name the same unreadable cluster many times, and one failed
		// read of it is enough. A deleted one is marked only once it is descended
		// into, because until then the cluster may belong to a live directory -
		// see DescendDeletedDirectories - whose subtree a stale claim would hide.
		if !entry.IsDeleted() {
			w.seen[entry.entryCluster] = struct{}{}
		}

		got, err := w.children(entry)
		if err != nil {
			// A cancellation noticed part-way through a deleted directory is the
			// walk stopping, not a fact about that directory.
			if ctxErr := w.prog.ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
				return err
			}
			if w.opts.StopOnReadError {
				return err
			}
			if serr := w.skip(path, entry, SkipReadError, err); serr != nil {
				return serr
			}
			continue
		}
		if got.declined {
			// A live directory declined is one ReadDir will not read. A deleted
			// one is either the caller's choice - the descent is off, which is not
			// the walk's finding to report - or the bitmap's refusal, which is.
			var serr error
			switch {
			case !entry.IsDeleted():
				serr = w.skip(path, entry, SkipReadError,
					fmt.Errorf("%w: ReadDir does not read a directory whose name is blank", ErrInvalidEntry))
			case w.opts.DescendDeletedDirectories:
				serr = w.skip(path, entry, SkipDeletedUnrecoverable, nil)
			}
			if serr != nil {
				return serr
			}
			continue
		}
		if entry.IsDeleted() {
			w.seen[entry.entryCluster] = struct{}{}
		}
		if got.cut != nil {
			if serr := w.skip(path, entry, SkipPartialRead, got.cut); serr != nil {
				return serr
			}
		}
		if err := w.report(got.entries, path+"/", entry.entryCluster, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// skip reports a directory the walk did not descend into, if the caller asked to
// hear about them. The nil check lives here so that the call sites in report read
// the same way.
func (w *treeWalk) skip(path string, entry Entry, reason SkipReason, err error) error {
	if w.opts.OnSkip == nil {
		return nil
	}
	return w.opts.OnSkip(path, entry, reason, err)
}

// listing is what children read of one directory.
type listing struct {
	entries []Entry
	// declined is set when nothing was read and that is not a failure: a deleted
	// directory the options or the bitmap do not permit descending into, or a
	// live one ReadDir will not read.
	declined bool
	// cut is set when only part of the directory could be read, and says why;
	// entries holds what came before it.
	cut error
}

// children reads a directory entry's contents.
func (w *treeWalk) children(entry Entry) (listing, error) {
	if entry.IsDeleted() {
		return w.deletedChildren(entry)
	}

	// ReadDir returns nothing and no error for an entry it will not parse. For a
	// live directory that gets this far - not deleted, not special - that means a
	// name that is only spaces, which the parser's $Unnamed placeholder does not
	// replace because the name is not empty. Saying so is what lets report tell
	// it from an empty directory.
	if entry.NonParsable() {
		return listing{declined: true}, nil
	}

	// This is ReadDir's read, with the one fact ReadDir does not return: whether
	// the end-of-directory record was reached.
	entries, complete, err := w.fs.readDirEntries(entry)
	if err != nil && !isEOF(err) {
		return listing{}, err
	}
	// The end of a truncated image is tolerated, as ReadDir tolerates it: it is
	// not a reason to lose the records that were there. It is only a loss when it
	// came before the end-of-directory record; a directory whose records had
	// already ended lost nothing to it.
	got := listing{entries: entries}
	if err != nil && !complete {
		got.cut = err
	}
	return got, nil
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
// directory's children. Stopping there after reading at least one cluster is a
// partial read, and the listing says so.
func (w *treeWalk) deletedChildren(entry Entry) (listing, error) {
	if !w.opts.DescendDeletedDirectories {
		return listing{declined: true}, nil
	}

	located, err := w.fs.FragmentOffsetsWithOptions(entry, FragmentOptions{})
	if err != nil {
		return listing{}, err
	}
	if located.FirstClusterReallocated {
		return listing{declined: true}, nil
	}

	parser := newDirParser(&w.fs.vbr, w.fs.optimistic, w.fs.rejectChecksumMismatch)
	parser.inDirectory(entry.entryCluster)

	state := w.fs.vbr.acquireVisit()
	defer w.fs.vbr.releaseVisit(state)

	var entries []Entry
	read := 0
	for _, run := range located.Ranges {
		for i := uint32(0); i < run.ClusterCount; i++ {
			cluster := run.StartCluster + i

			// An unreadable bitmap is not evidence that the cluster was reused,
			// so it does not stop the descent; a bitmap that says "in use" is.
			if allocated, bitmapErr := w.fs.IsClusterAllocated(cluster); bitmapErr == nil && allocated {
				if read == 0 {
					return listing{declined: true}, nil
				}
				return listing{
					entries: entries,
					cut:     fmt.Errorf("%w: cluster %d is allocated again", ErrDeletedEntry, cluster),
				}, nil
			}

			if err := w.prog.check(); err != nil {
				return listing{}, err
			}

			buf := state.ensureBuf(&w.fs.vbr)
			if err := w.fs.vbr.readClusterInto(cluster, buf); err != nil {
				return listing{}, err
			}
			read++
			if parser.parseDirChunk(cluster, buf, &entries) {
				return listing{entries: entries}, nil
			}
		}
	}
	return listing{entries: entries}, nil
}

// recovered reports the entries carved out of clusters that no path reaches,
// after the whole reachable tree.
func (w *treeWalk) recovered() error {
	carved, result, err := w.fs.recoverDeleted(w.prog)
	if err != nil {
		return err
	}
	// The sweep's own account of what it could not cover, which the entries
	// alone do not carry. Before the entries, so that a caller refusing a
	// truncated sweep refuses it whole.
	if w.opts.OnRecoveredScan != nil {
		if herr := w.opts.OnRecoveredScan(&result); herr != nil {
			return herr
		}
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
