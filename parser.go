package libxfat

// dirParser is the state of one directory parse.
//
// It exists so that *ExFAT carries no mutable parse state, which is what lets two
// goroutines read two directories of one volume. The sibling FAT library reaches
// the same property the same way, by keeping a traversal's state in its own value
// rather than on the volume handle.
//
// A dirParser is created per parse and discarded afterwards. Nothing it holds is
// handed out: the names a caller receives are strings copied out of nameBytes, so
// the buffers below can be reused freely without a caller ever observing it.
type dirParser struct {
	// v is geometry and the backing reader. It is never written through here.
	v *VBR

	// The validation policy is copied at construction, so a parse in flight
	// cannot observe a change to the volume's settings partway through.
	optimistic             bool
	rejectChecksumMismatch bool

	// Per-record cursor within the chunk being parsed.
	offset  int
	dirtype byte

	// Entry-set assembly. An entry set spans several 32-byte records, and may
	// span a cluster boundary, so this state outlives a single record and a
	// single chunk.
	entry            Entry
	virtualEntry     Entry
	entryState       int
	remainingSC      int
	setChecksum      uint16
	expectedChecksum uint16
	expectedSC       int
	expectedNameLen  int
	nameUnits        []uint16
	nameBytes        []byte
	setInUse         bool
	sawStream        bool

	// Where the bytes being parsed live, so that an entry can carry its own
	// address rather than leaving the caller to reconstruct one.
	//
	// parentCluster describes the directory and outlives the whole parse;
	// chunkCluster and slotBase describe the chunk currently in hand and advance
	// with it. A fragmented directory needs no special handling here: the cluster
	// is whichever one the chain walk actually reached, so an offset derived from
	// it is where the bytes are rather than where a contiguous directory would
	// have put them.
	parentCluster uint32
	chunkCluster  uint32
	slotBase      uint32

	// out collects what the parse learned about the volume rather than about any
	// one entry.
	out parseOutputs
}

// unlocatedChunk is what a caller passes for a chunk it cannot place on the
// image - a hand-built test fixture, or a buffer assembled from somewhere other
// than the cluster heap. Cluster 0 is not addressable on exFAT, where the heap
// starts at 2, so it cannot be mistaken for a real location: entries parsed from
// such a chunk report no entry-set offset, rather than a plausible wrong one.
const unlocatedChunk uint32 = 0

// parseOutputs are the facts a directory parse discovers about the volume itself:
// its label, and where its $BitMap and $UpCase streams live.
//
// They are collected here and published by the caller rather than written into
// shared state from inside the record loop. That is not only a concurrency
// concern. These are root-directory records by specification, and publishing them
// from wherever they happened to be parsed meant a $BitMap record in a
// subdirectory silently repointed the allocation bitmap for the whole volume -
// and with it every answer RecoverDeletedEntries gives.
type parseOutputs struct {
	volumeLabel string
	sawLabel    bool
	bitmapEntry Entry
	sawBitmap   bool
	upcaseEntry Entry
	sawUpcase   bool
}

// newDirParser starts a parse over the volume described by v.
func newDirParser(v *VBR, optimistic, rejectChecksumMismatch bool) *dirParser {
	p := &dirParser{
		v:                      v,
		optimistic:             optimistic,
		rejectChecksumMismatch: rejectChecksumMismatch,
	}
	p.resetDirParser()
	return p
}

// resetDirParser returns the parser to its starting state, ready for a fresh
// directory.
func (p *dirParser) resetDirParser() {
	p.entry = Entry{}
	p.virtualEntry = Entry{}
	p.offset = 0
	p.remainingSC = 0
	p.entryState = ENTRY_STATE_START
	p.chunkCluster = unlocatedChunk
	p.slotBase = 0
	p.resetSetAssembly()
}

// inDirectory names the directory whose contents the parser is about to read, so
// that the entries it yields can name their parent.
//
// It is deliberately not part of resetDirParser: which directory is being read is
// a property of the whole parse, while the chunk position resets with every
// chunk, and RecoverDeletedEntries resets between clusters that have no parent
// directory at all.
func (p *dirParser) inDirectory(firstCluster uint32) {
	p.parentCluster = firstCluster
}

// recordOffset is the absolute image offset of the record under the parser's
// cursor, or zero when the chunk in hand was not placed on the image.
func (p *dirParser) recordOffset() int64 {
	if !p.v.isValidCluster(p.chunkCluster) {
		return 0
	}
	base, err := safeInt64(p.v.getClusterOffset(p.chunkCluster))
	if err != nil {
		return 0
	}
	return base + int64(p.offset)
}

// addressRecord stamps the address of the record under the cursor onto entry.
//
// The slot index is logical - a count of 32-byte slots from the start of the
// directory - and so survives the parent directory being relocated, which the
// absolute offset does not. Both are recorded because they answer different
// questions: one identifies the entry, the other says where to go and read it.
func (p *dirParser) addressRecord(entry *Entry) {
	entry.parentFirstCluster = p.parentCluster
	entry.entrySlotIndex = p.slotBase + uint32(p.offset/EXFAT_DIRRECORD_SIZE)
	entry.entrySetOffset = p.recordOffset()
}

func (p *dirParser) resetSetAssembly() {
	p.setChecksum = 0
	p.expectedChecksum = 0
	p.expectedSC = 0
	p.expectedNameLen = 0
	// Truncated rather than dropped: the parser owns this buffer, never hands
	// it out, and reassembles a name into it for every entry set on the volume.
	p.nameUnits = p.nameUnits[:0]
	p.setInUse = false
	p.sawStream = false
}

// beginEntrySet starts assembling a new file entry set from its primary record.
// The pending entry is cleared first: an earlier set that was abandoned partway
// through - a truncated directory, a damaged secondary - would otherwise leave
// its cluster and length behind for this one to inherit.
func (p *dirParser) beginEntrySet(rec dirRecordView) {
	p.entry = Entry{}
	p.resetSetAssembly()

	p.setChecksum = exfatDirSetChecksumAdd(0, rec.data, true)
	p.setInUse = entryInUse(rec.typeByte())
	p.populateDirRecordDel(rec)
	p.expectedSC = int(p.entry.secondaryCount)
	p.expectedChecksum = uint16(rec.byteAt(2)) | (uint16(rec.byteAt(3)) << 8)
}

// secondaryBelongsToSet reports whether a secondary record's allocation state
// agrees with the primary that opened the set. Type validation masks off the
// in-use bit, so without this a 0x41 name record would be accepted into an
// allocated 0x85 set, splicing a deleted name onto a live file.
func (p *dirParser) secondaryBelongsToSet(rec dirRecordView) bool {
	return p.entryState == ENTRY_STATE_85_SEEN && entryInUse(rec.typeByte()) == p.setInUse
}

// finishEntrySet completes the pending entry set and appends it to entries.
//
// The assembled name is always stored. A checksum mismatch says the set is
// damaged, which is a fact worth reporting about the entry - it is not a reason
// to replace the only copy of the name with an empty string, which is what this
// used to do in strict mode, silently rendering every entry on the volume
// nameless and every directory unreadable.
func (p *dirParser) finishEntrySet(entries *[]Entry) {
	// Name records are fixed at 15 code units each, so the last one is padded
	// past the end of the name. A formatter zeroes that padding, but a record
	// reused by a later, shorter name need not: NameLength is what says where
	// the name actually stops, and without this the residue is read as part of
	// it.
	if p.expectedNameLen > 0 && len(p.nameUnits) > p.expectedNameLen {
		p.nameUnits = p.nameUnits[:p.expectedNameLen]
	}

	checked := !p.optimistic
	verified := p.expectedChecksum == p.setChecksum

	// Decoded through a buffer the parser owns, so the name costs the one
	// allocation the caller keeps and nothing else.
	p.nameBytes = appendUTF16AsUTF8(p.nameBytes[:0], dropNULs(p.nameUnits))
	name := string(p.nameBytes)
	p.entry.rawName = name

	// A set whose name records are empty or all NUL leaves nothing to call the
	// entry by, and a directory with no name is treated as unreadable, so the
	// whole subtree below it would disappear without a word. An entry is located
	// by its cluster, not by its name, so there is no reason to lose it: stand a
	// placeholder in, keyed to the cluster so it is stable across runs, and
	// record that the name did not come off the disk.
	if name == "" {
		name = unnamedEntryName(p.entry.entryCluster)
		p.entry.nameSynthetic = true
	}

	p.entry.name = name
	p.entry.nameChecksumChecked = checked
	p.entry.nameChecksumVerified = verified
	p.entry.expectedSetChecksum = p.expectedChecksum
	p.entry.computedSetChecksum = p.setChecksum

	if p.entry.IsDeleted() {
		p.entry.name += DELETED
	}

	drop := checked && !verified && p.rejectChecksumMismatch
	if !drop {
		*entries = append(*entries, p.entry)
	}

	p.entry = Entry{}
	p.entryState = ENTRY_STATE_LAST_C1_SEEN
	p.resetSetAssembly()
}

func (p *dirParser) clearParsedEntry() {
	p.entry = Entry{}
	p.entryState = ENTRY_STATE_START
	p.remainingSC = 0
	p.resetSetAssembly()
}

// parseDir parses a single directory chunk that the caller cannot place on the
// image. Entries it yields carry no entry-set offset; see unlocatedChunk.
func (p *dirParser) parseDir(clusterdata []byte) []Entry {
	var entries []Entry
	p.resetDirParser()
	p.parseDirChunk(unlocatedChunk, clusterdata, &entries)
	return entries
}

// parseDeletedDirEntries carves file entry sets out of the cluster numbered
// cluster, which is expected to be unallocated.
//
// The entries it yields are addressed but not identified: cluster says where
// their records are, while the directory that listed them is gone, so they have
// no parent and no slot index that would mean anything. FileID refuses them for
// exactly that reason.
func (p *dirParser) parseDeletedDirEntries(cluster uint32, clusterdata []byte) []Entry {
	var entries []Entry
	p.resetDirParser()
	p.parentCluster = 0
	p.chunkCluster = cluster

	for offset := 0; offset+EXFAT_DIRRECORD_SIZE <= len(clusterdata); offset += EXFAT_DIRRECORD_SIZE {
		rec, ok := newDirRecordView(clusterdata, offset)
		if !ok {
			break
		}
		p.offset = offset
		p.dirtype = rec.typeByte()

		if p.dirtype == 0 {
			p.clearParsedEntry()
			continue
		}

		if (p.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_FILEDIR {
			p.clearParsedEntry()
			if p.v.validateFileDentry(rec.data) {
				p.beginEntrySet(rec)
			}
			continue
		}

		if (p.dirtype&0x7f) == EXFAT_DIRRECORD_DEL_STREAM_EXT && p.secondaryBelongsToSet(rec) {
			if !p.sawStream && p.v.validateFileStreamDentry(rec.data) {
				p.setChecksum = exfatDirSetChecksumAdd(p.setChecksum, rec.data, false)
				p.populateDirRecordStreamSeen(rec)
				p.expectedNameLen = int(p.entry.nameLen)
				p.sawStream = true
			}
			continue
		}

		if (p.dirtype&0x7f) == EXFAT_DIRRECORD_DEL_FILENAME_EXT && p.secondaryBelongsToSet(rec) {
			if !p.v.validateFileNameDentry(rec.data) {
				continue
			}

			p.setChecksum = exfatDirSetChecksumAdd(p.setChecksum, rec.data, false)
			p.nameUnits = appendUTF16LEUnits(p.nameUnits, rec.bytes(2, EXFAT_DIRRECORD_SIZE), 15)

			if p.remainingSC < 1 {
				continue
			}
			p.remainingSC--
			if p.remainingSC != 0 {
				continue
			}

			// A set with no stream extension has no cluster, no length and no
			// name length; emitting it would invent a file that is not there.
			if !p.sawStream {
				p.clearParsedEntry()
				continue
			}

			p.finishEntrySet(&entries)
			p.clearParsedEntry()
		}
	}

	return entries
}

// parseDirChunk parses one chunk of a directory, read from the cluster numbered
// cluster, and reports whether the end of the directory was reached.
//
// Successive calls continue one directory, so the slot index keeps counting
// across the chunk boundary. It advances only on the path that runs off the end
// of the chunk, since the other two mean there is no next chunk to number.
func (p *dirParser) parseDirChunk(cluster uint32, clusterdata []byte, entries *[]Entry) bool {
	p.offset = 0
	p.chunkCluster = cluster

	for p.offset < len(clusterdata) {
		if clusterdata[p.offset] == 0 {
			return true
		}

		rec, ok := newDirRecordView(clusterdata, p.offset)
		if !ok {
			return true
		}

		p.dirtype = rec.typeByte()

		switch p.dirtype {
		case EXFAT_DIRRECORD_LABEL:
			// Validate volume label/no-label entry
			if p.v.validateVolLabelDentry(rec.data) {
				p.populateDirRecordLabel(rec)
			}
		case EXFAT_DIRRECORD_NOLABEL:
			p.entry.name = ""
		case EXFAT_DIRRECORD_BITMAP, EXFAT_DIRRECORD_UPCASE:
			if (p.dirtype == EXFAT_DIRRECORD_BITMAP && p.v.validateAllocBitmapDentry(rec.data)) ||
				(p.dirtype == EXFAT_DIRRECORD_UPCASE && p.v.validateUpcaseTableDentry(rec.data)) {
				p.populateRecordBitmapUpcase(rec)
				*entries = append(*entries, p.virtualEntry)
			}
		// These three records carry no content stream. They must be built from a
		// clean Entry rather than by patching the shared virtualEntry, which
		// still holds the cluster and length of whichever $BitMap or $UpCase
		// record was parsed before them.
		case EXFAT_DIRRECORD_VOLUME_GUID:
			p.virtualEntry = Entry{etype: p.dirtype, name: VOLUME_GUID}
			p.addressRecord(&p.virtualEntry)
			*entries = append(*entries, p.virtualEntry)
		case EXFAT_DIRRECORD_TEXFAT:
			p.virtualEntry = Entry{etype: p.dirtype, name: TEXFAT}
			p.addressRecord(&p.virtualEntry)
			*entries = append(*entries, p.virtualEntry)
		case EXFAT_DIRRECORD_ACT:
			p.virtualEntry = Entry{etype: p.dirtype, name: ACT}
			p.addressRecord(&p.virtualEntry)
			*entries = append(*entries, p.virtualEntry)
		default:
			if (p.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_FILEDIR {
				if p.v.validateFileDentry(rec.data) {
					p.beginEntrySet(rec)
				}
			}
			if ((p.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_STREAM_EXT) &&
				p.secondaryBelongsToSet(rec) {
				if !p.sawStream && p.v.validateFileStreamDentry(rec.data) {
					p.setChecksum = exfatDirSetChecksumAdd(p.setChecksum, rec.data, false)
					p.populateDirRecordStreamSeen(rec)
					p.expectedNameLen = int(p.entry.nameLen)
					p.sawStream = true
				}
			}
			// Name records are folded into the checksum only while a set is
			// open. A stray 0xC1 outside one - slack, or a directory whose
			// primary record was never parsed - would otherwise corrupt the
			// running checksum and prepend its bytes to the next real name.
			if ((p.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_FILENAME_EXT) &&
				p.secondaryBelongsToSet(rec) {
				if p.v.validateFileNameDentry(rec.data) {
					p.setChecksum = exfatDirSetChecksumAdd(p.setChecksum, rec.data, false)

					raw := rec.bytes(2, EXFAT_DIRRECORD_SIZE)
					p.nameUnits = appendUTF16LEUnits(p.nameUnits, raw, 15)

					if p.remainingSC >= 1 {
						p.remainingSC--

						if p.remainingSC == 0 {
							if p.sawStream {
								p.finishEntrySet(entries)
							} else {
								p.clearParsedEntry()
							}
						}
					}
				}
			}
		}

		p.offset += EXFAT_DIRRECORD_SIZE
	}

	p.slotBase += uint32(len(clusterdata) / EXFAT_DIRRECORD_SIZE)
	return false
}

func (p *dirParser) populateDirRecordLabel(rec dirRecordView) {
	count := int(rec.byteAt(1))
	endOffset := 2 + count*2
	if endOffset > len(rec.data) {
		endOffset = len(rec.data)
	}
	p.out.volumeLabel = unicodeFromAscii(rec.bytes(2, endOffset), count)
	p.out.sawLabel = true
}

func (p *dirParser) populateRecordBitmapUpcase(rec dirRecordView) {
	entryCluster := rec.le32(20)
	dataLen := rec.le64(24)

	p.addressRecord(&p.virtualEntry)
	p.virtualEntry.etype = p.dirtype
	p.virtualEntry.dataLen = dataLen
	p.virtualEntry.entryCluster = entryCluster

	// no real dates/times
	p.virtualEntry.modified = 0
	p.virtualEntry.created = 0
	p.virtualEntry.accessed = 0
	p.virtualEntry.modified10ms = 0
	p.virtualEntry.created10ms = 0
	p.virtualEntry.modifiedUtcOffset = 0
	p.virtualEntry.createdUtcOffset = 0
	p.virtualEntry.accessedUtcOffset = 0
	p.virtualEntry.entryAttr = 0
	p.virtualEntry.secondaryCount = 0
	p.virtualEntry.noFatChain = false
	p.virtualEntry.isRegion = false
	p.virtualEntry.regionOffset = 0
	p.virtualEntry.validDataLen = dataLen

	switch p.dirtype {
	case EXFAT_DIRRECORD_BITMAP:
		p.virtualEntry.name = BITMAP
		p.out.bitmapEntry = p.virtualEntry
		p.out.sawBitmap = true
	case EXFAT_DIRRECORD_UPCASE:
		p.virtualEntry.name = UPCASE
		p.out.upcaseEntry = p.virtualEntry
		p.out.sawUpcase = true
	}
}

func (p *dirParser) populateDirRecordDel(rec dirRecordView) {
	p.addressRecord(&p.entry)
	p.entry.etype = p.dirtype
	p.entry.secondaryCount = uint32(rec.byteAt(1))
	p.entry.entryAttr = rec.le16(4)
	p.entry.created = rec.le32(8)
	p.entry.modified = rec.le32(12)
	p.entry.accessed = rec.le32(16)
	p.entry.created10ms = rec.byteAt(20)
	p.entry.modified10ms = rec.byteAt(21)
	// Without these the timestamps above are unanchored wall-clock readings.
	p.entry.createdUtcOffset = rec.byteAt(22)
	p.entry.modifiedUtcOffset = rec.byteAt(23)
	p.entry.accessedUtcOffset = rec.byteAt(24)
	p.remainingSC = int(p.entry.secondaryCount)
	// Both 0x85 (allocated) and 0x05 (deleted) begin a file entry set.
	if (p.dirtype & 0x7f) == EXFAT_DIRRECORD_DEL_FILEDIR {
		p.entryState = ENTRY_STATE_85_SEEN
	}
}

func (p *dirParser) populateDirRecordStreamSeen(rec dirRecordView) {
	p.entry.nameLen = rec.byteAt(3)
	p.entry.nameHash = rec.le16(4)
	p.entry.entryCluster = rec.le32(20)
	p.entry.dataLen = rec.le64(24)
	// ValidDataLength lives at offset 8 of the stream extension entry, not 24.
	// Reading it from 24 made it a duplicate of DataLength, which hides the
	// allocated-but-never-written tail that slack analysis depends on.
	p.entry.validDataLen = rec.le64(8)

	p.entry.noFatChain = false
	if (rec.byteAt(1) & NOT_FAT_CHAIN_FLAG) != 0 {
		p.entry.noFatChain = true
	}

	p.remainingSC--
}
