package libxfat

// Capabilities reports what an exFAT volume can record, as distinct from what it
// happens to record.
//
// It exists so that a consumer can tell "this format does not keep that" from
// "that was absent here". Without it a pass comparing two readings of a volume has
// no way to know that exFAT keeps no metadata-change time, and a report merging
// several filesystems would show exFAT files as having lost permissions they never
// had.
//
// Most of these are properties of the format and identical for every exFAT volume.
// They are still fields rather than documentation so that a caller can branch on a
// value instead of on a string naming the filesystem. The few that depend on the
// volume in hand say so.
type Capabilities struct {
	// UnicodeNames reports that names are stored as UTF-16 code units. exFAT
	// always does, which is why VerifyNameHash needs the volume's up-case table
	// rather than an ASCII fold.
	UnicodeNames bool `json:"unicode_names"`
	// CaseSensitive reports whether name comparison distinguishes case. exFAT is
	// case-insensitive and case-preserving, so two names differing only in case
	// cannot coexist even though both spellings are recorded faithfully.
	CaseSensitive bool `json:"case_sensitive"`

	// CreationTimes, ModificationTimes and AccessTimes report which timestamps the
	// directory records carry. exFAT has all three.
	CreationTimes     bool `json:"creation_times"`
	ModificationTimes bool `json:"modification_times"`
	AccessTimes       bool `json:"access_times"`
	// MetadataChangeTimes reports whether a record carries the moment its own
	// metadata last changed - ctime on a POSIX filesystem. exFAT has no such
	// field, so a rename or an attribute change can leave no timestamp behind at
	// all, and a consumer must not read the absence of one as evidence that
	// nothing happened.
	MetadataChangeTimes bool `json:"metadata_change_times"`
	// SubSecondTimestamps reports that some timestamps carry a fraction of a
	// second. exFAT records 10-millisecond increments for creation and
	// modification but not for access, which is therefore always on a two-second
	// boundary; Entry.Timestamps is where that asymmetry is visible.
	SubSecondTimestamps bool `json:"sub_second_timestamps"`
	// TimezoneOffsets reports that timestamps can be anchored to UTC by an offset
	// stored beside them. exFAT can, but each record says whether its own offset
	// is valid, so this is the format's capability and not a promise about any
	// entry. Entry.Timestamps reports which readings were anchored.
	TimezoneOffsets bool `json:"timezone_offsets"`

	// POSIXPermissions, HardLinks, SymbolicLinks, ExtendedAttributes, SparseFiles
	// and Compression are all absent from exFAT. A file is a name, an attribute
	// byte and a run of clusters; there is no owner, no link count, no hole and no
	// second stream.
	POSIXPermissions   bool `json:"posix_permissions"`
	HardLinks          bool `json:"hard_links"`
	SymbolicLinks      bool `json:"symbolic_links"`
	ExtendedAttributes bool `json:"extended_attributes"`
	SparseFiles        bool `json:"sparse_files"`
	Compression        bool `json:"compression"`

	// StableFileIdentity reports whether the volume records a file identity that
	// survives being reused - an inode number with a generation count, or anything
	// equivalent. exFAT records none, which makes this the most consequential
	// entry in this struct for a consumer diffing two readings of a volume: a
	// FileID names a slot in a directory, and a slot reused after a deletion
	// carries its predecessor's FileID exactly. See FileID for what follows.
	StableFileIdentity bool `json:"stable_file_identity"`
	// IdentityReuseCounter reports whether a reused identity can be recognised as
	// reused. It cannot. It is stated separately from StableFileIdentity so that a
	// consumer checking only for the presence of an identity does not mistake the
	// pair libxfat does supply for one that carries a generation.
	IdentityReuseCounter bool `json:"identity_reuse_counter"`

	// AllocationBitmap reports that free space is recorded independently of the
	// FAT, in the $BitMap stream. exFAT has one, which is what makes
	// FragmentResult.FirstClusterReallocated answerable: a deleted entry's first
	// cluster can be tested for reuse without reference to a chain that has
	// already been freed.
	AllocationBitmap bool `json:"allocation_bitmap"`
	// ValidDataLength reports that a record distinguishes the bytes ever written
	// from the bytes allocated. exFAT does, which is why UnwrittenRanges can name
	// the allocated-but-never-written tail of a file rather than leaving it
	// indistinguishable from content.
	ValidDataLength bool `json:"valid_data_length"`
	// DeclaredContiguity reports that a record can state that its stream occupies
	// consecutive clusters, leaving its FAT entries undefined. exFAT's NoFatChain
	// flag does exactly that, which is why a deleted contiguous file can be located
	// in full without assuming anything - see Entry.IsContiguous.
	DeclaredContiguity bool `json:"declared_contiguity"`
	// DeletedEntriesSurvive reports that deleting a file leaves its directory
	// record in place, marked rather than erased. exFAT clears one bit of the type
	// byte, so the name, the size, the timestamps, the first cluster and the
	// contiguity flag all survive it.
	DeletedEntriesSurvive bool `json:"deleted_entries_survive"`

	// SecondFAT reports whether this volume carries two FATs, which only a TexFAT
	// volume does. It is a fact about the volume in hand rather than about the
	// format. When it is true, ActiveFAT says which of the two chain walks read,
	// and the $FAT1 and $FAT2 virtual entries expose both for comparison.
	SecondFAT bool `json:"second_fat"`
	// Journal reports whether the volume has a journal this library can read. None
	// does. TexFAT's second FAT and second allocation bitmap give a writer a way
	// to commit atomically, but they are a redundant copy rather than a record of
	// what changed, so they cannot be read back as a history.
	Journal bool `json:"journal"`
}

// Capabilities returns what this volume's format records.
//
// Every field is either fixed by exFAT itself or, where the field says so, read
// from this volume's boot record. Nothing here reads the volume's directories, so
// it is cheap and cannot fail.
func (e *ExFAT) Capabilities() Capabilities {
	if e == nil {
		return Capabilities{}
	}
	return Capabilities{
		UnicodeNames:      true,
		CaseSensitive:     false,
		CreationTimes:     true,
		ModificationTimes: true,
		AccessTimes:       true,
		// The false values below are written out rather than left to the zero
		// value, because each one is an answer this type exists to give.
		MetadataChangeTimes: false,
		SubSecondTimestamps: true,
		TimezoneOffsets:     true,

		POSIXPermissions:   false,
		HardLinks:          false,
		SymbolicLinks:      false,
		ExtendedAttributes: false,
		SparseFiles:        false,
		Compression:        false,

		StableFileIdentity:   false,
		IdentityReuseCounter: false,

		AllocationBitmap:      true,
		ValidDataLength:       true,
		DeclaredContiguity:    true,
		DeletedEntriesSurvive: true,

		SecondFAT: e.vbr.numberOfFats > 1,
		Journal:   false,
	}
}
