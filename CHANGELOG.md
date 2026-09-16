# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.4.0] - 2026-09-16

The Go API is additive: no exported symbol was removed, renamed or
re-signatured, and no JSON key was removed or renamed. Reports gain three keys
and every extent gains one, which a consumer that ignores unknown keys is
unaffected by. The module path is unchanged and stays at v1.

The one theoretical source of friction is that `Range` gains a field, so an
unkeyed composite literal - `Range{a, b, c, d, e}` - no longer compiles. A keyed
literal, which is what any code outside this package should have been using,
is unaffected.

### Added

- **File-relative offsets on runs.** `Range.FileOffset` (`file_offset`) is where
  a run begins in the file's own byte space, so mapping a changed image range
  back to a position inside the file no longer requires the caller to accumulate
  lengths. Deriving it by hand is only correct if you know no run is ever elided
  - true on exFAT, not true in general - and getting it wrong yields a plausible
  wrong answer rather than an error, which is the failure this field removes.
  `Coalesce` renumbers the runs it returns.
- The field is placed and named to match the sibling `libfat`, whose `Range` is
  again identical to this one field for field. The two libraries agreeing is
  worth more than either being marginally different.
- `SlackRange` reports the entry's recorded data length as its `FileOffset`,
  slack being what follows the last byte of content, and `UnwrittenRanges`
  numbers its parts from `ValidDataLength`. Both are stated in the field's
  documentation, since neither is a position inside the file.
- `Range`'s documentation now states its semantics in full, as the list of
  questions an adapter author would otherwise answer by reading the
  implementation: offsets are image-absolute and include `Source.Base`, `Length`
  excludes cluster slack, holes do not occur, the `ValidDataLength` boundary does
  not split a run, adjacent runs are coalesced, and the slice is in file order,
  sorted by `FileOffset` and gap-free.
- **Report schema version and provenance.** `ExFATReport` gains `SchemaVersion`
  (`schema_version`, currently `1`, exposed as the constant
  `ReportSchemaVersion`), `LibraryVersion` (`library_version`, the constant
  `LibraryVersion`) and `Generated` (`generated`). A forensic report is evidence
  and outlives the tool that produced it; without a schema version a consumer
  can neither pin what it understands nor detect a document written by a version
  it does not. `SchemaVersion` increments when a field is removed, renamed or
  changes meaning, never when one is added.
- `Generated` is wall-clock time and so the only field that differs between two
  reports of an unchanged volume. A consumer hashing a report to detect change
  must exclude it. It is unrelated to `ExFATMeta.Revision`, which is the volume's
  own on-disk format revision.

### Changed

- The report's `FileFragment.FileOffset` is now read from the `Range` rather than
  derived while converting, so the document and the API cannot disagree about
  where a run sits in the file. The emitted values are unchanged.

## [1.3.0] - 2026-09-14

The module path is unchanged: `github.com/aoiflux/libxfat`. This release renames
and removes exported API, so a consumer pinned to an earlier tag keeps working
and one upgrading has changes to make. The Changed and Removed sections below are
the whole list.

The release exists to make libxfat usable for two things it could not do before:
mapping a file to byte ranges without reading it, and reading file content without
writing it to disk. Most of the rest follows from removing what stood in the way.

### Fixed

- **A record in any subdirectory could repoint the volume's `$BitMap` or
  `$UpCase`.** The parse case for the `0x81` and `0x82` records ran for every
  directory, not just the root, and wrote its result onto state shared by the whole
  volume. So a `0x81` record anywhere on the volume redefined the allocation
  bitmap, and with it every answer `RecoverDeletedEntries` gives - which clusters
  it reads, and therefore what it reports as deleted. Per the specification these
  are root-directory records; only the root parse publishes them now.
  `validateAllocBitmapDentry` constrained the target to a plausible bitmap, so this
  was never arbitrary redirection, but it was enough to make the library carve
  deleted entries out of a region an image chose.

- **Copying an `ExFAT` value handed the same scratch buffer to two live users.**
  Two copies each released per-walk scratch at the same index of a shared array and
  then took back what each believed was its own. The constructors now return
  `*ExFAT`, and the struct carries a mutex, so `go vet` reports any copy.

- **`ClusterList` read the whole file to enumerate its clusters.** The chain walk
  read every cluster's data before handing it to a visitor that discarded it, so
  enumerating one file's clusters read that file, and enumerating a volume's read
  the volume. Chain walking now reads only the four-byte FAT entries, through a
  buffered window: 31.5us and 3 allocations per call down to 10.7us and 1 on a
  two-cluster file, and zero bytes of file data read at any size.

- **Extraction followed `..` in a name out of the destination directory.** A name
  on an exFAT volume is whatever its records say, including `..`, and joining one
  onto an output path climbed out of it. `ExtractAllFiles` now refuses a name that
  resolves outside the destination. The entry is still reported by `Walk`.

- `VolumeSerialNumber` was parsed as a four-byte slice into the boot-region parse
  buffer, keeping all twelve sectors of it alive to hold a number nothing read.

- **Chain walks read the FAT the volume says is live.** `ActiveFAT` reported which
  of a TexFAT volume's two tables the `VolumeFlags` select, while every chain walk
  read the first table regardless - a gap the accessor's own documentation admitted
  to rather than closed. On such a volume every chain libxfat reported came from the
  copy the volume had superseded. The flag is honoured only where a second FAT
  exists to select: a volume recording one FAT and an active index of 1 is
  malformed, and believing it would point every walk just past the FAT region, at
  the cluster heap, read as though it were a table of cluster numbers.
  `ActiveFatOffset` is the address that follows, and `ActiveFAT` still reports the
  index the volume recorded even where it cannot be acted on.

### Added

- **A JSON report.** `Report`, `ReportDeep`, `ReportWithOptions`,
  `ReportWithOptionsContext` and the four `WriteReport` forms, producing an
  `ExFATReport`: an `ExFATMeta` block for the volume and one `ExFATFile` row per
  entry, each carrying its identity, its extents as `FileFragment`s, and a
  `FragmentProvenance` saying how those extents were derived. `Summary`,
  `FilterFiles`, `FilesByType`, `DeletedFiles`, `RecoveredFiles`,
  `FragmentedFiles` and `AssumedFiles` read a report back. libxfat had no report
  type at all, so a consumer that does not link against the library had no way to
  receive any of this.

  Two encoding rules, both deliberately verbose. An identity scalar is never
  omitted when zero, because slot 0 is the first slot of every directory and an
  `entry_absolute_offset` of -1 is a positive statement that the record could not
  be located. A provenance flag is never omitted when false, because a missing
  `chain_walked` would leave a reader unable to tell "these extents did not come
  from a FAT walk" from "this version does not emit that field", and a hypothesis
  would become indistinguishable from a fact.

  `ReportDeep` searches more places and never weakens the evidence: it does not set
  `AssumeContiguous`, so no row carries a synthesised extent unless the caller asked
  for one. The meta block reports the recorded `PercentInUse` hint beside the count
  from the allocation bitmap, and `BitmapError` when the bitmap could not be read at
  all, so a zero count cannot be mistaken for an empty volume.

- **`Capabilities`.** What exFAT records, as opposed to what a volume happens to
  hold, as a value rather than as documentation. The negative answers are the
  point: no metadata-change time, no POSIX ownership, no extended attributes, no
  sparse allocation, and - the consequential one - no reusable file identity, so
  `StableFileIdentity` and `IdentityReuseCounter` are both false and a `FileID` is
  an address rather than an identity. Without this a consumer cannot distinguish
  "this format does not keep that" from "that was absent here", and reports a
  change that never happened. `SecondFAT` is the one field read from the volume in
  hand. The block is embedded in every report, so a stored document still says what
  its filesystem could record.

- **`Entry.AllocationPossible` and `Entry.SecondaryFlags`, and
  `FragmentResult.AllocationContradiction`.** Bit 0 of `GeneralSecondaryFlags` says
  whether a stream extension's `FirstCluster` and `DataLength` mean anything at all;
  the parser tested only bit 1, so a record naming a first cluster while declaring
  no allocation possible was read as though it agreed with itself. The extents are
  still located - a cluster number written into a record is a lead whether or not
  the flag beside it agrees - and the contradiction is now reported alongside them.
  `SecondaryFlags` quotes the byte whole, so a bit this library does not interpret
  still reaches a report, for the same reason `VolumeFlags` exists.

- **Extent mapping.** `Range`, `FragmentResult`, `FragmentOptions`,
  `FragmentOffsets`, `FragmentOffsetsWithOptions`, `IsFragmented`, `SlackRange`,
  `UnwrittenRanges`, `Coalesce`, `TotalLength`, `IsClusterAllocated`. Absolute byte
  offsets, coalesced into runs, with the provenance of the answer reported rather
  than assumed: `ChainWalked`, `NoFatChain`, `Assumed`, `Truncated`, `ChainBroken`,
  `LoopDetected`, `FirstClusterReallocated`, `ValidBytes`. Field names and JSON tags
  match the sibling FAT library, so one consumer adapter serves both.

  A degraded chain returns the runs it established plus flags, where the cluster API
  still returns an error. Errors at the cluster boundary, flags at the extent
  boundary, one engine behind both.

- **Content reading.** `OpenEntry`, `ReadEntry`, and `*File` with `Read`, `ReadAt`,
  `ReadAll`, `WriteTo`, `Seek`, `Reader`, `ReaderAt`, `SectionReader`, `Size`,
  `ValidSize`, `Fragments`, `Slack`, `Unwritten`. Reads clamp to the bytes actually
  located, never to the size the directory entry records.

- **`Walk` and `WalkWithOptions`.** One cancellable pre-order pass in disk order,
  with a cycle guard, a depth cap, composed paths and parent identity. `Entry` stays
  immutable - the path is a callback argument, not something written into the name.
  `WalkOptions` opts into deleted records, into the surviving children of a deleted
  directory, and into the free-space sweep, whose carvings are reported under
  `RecoveredPath`.

- **Entry identity.** `FileID`, `Entry.EntrySetOffset`, `Entry.ParentFirstCluster`,
  `Entry.EntrySlotIndex`. The slot index is logical, so it survives the parent
  directory being relocated where a byte offset does not. Documented as
  best-effort: a slot reused after a deletion carries its predecessor's identity
  exactly, and nothing on an exFAT volume distinguishes the two.

- **Volume identity and geometry**, all of it previously parsed and unreachable:
  `VolumeSerialNumber`, `FilesystemRevision`, `Base`, `PartitionOffset`,
  `VolumeSize`, `BytesPerSector`, `SectorsPerCluster`, `ClusterCount`,
  `RootDirCluster`, `ClusterHeapOffset`, `FatOffset`, `FatSize`, `FatCount`,
  `PercentInUse`.

- **`VolumeFlags` is parsed at all**, which it was not: `VolumeFlags`, `ActiveFAT`,
  `VolumeDirty`, `MediaFailure`. `VolumeDirty` is direct evidence that the volume
  was not cleanly unmounted, and a caveat on everything else it says.

- `RecoverDeletedEntriesContext`, and `ErrNilContext` / `ErrNilCallback`.

- New error sentinels `ErrTruncatedChain`, `ErrFragmented`, `ErrNoDataClusters`.
  `ErrInvalidCluster` and `ErrOutOfBounds` are reused where they already fit, so
  existing `errors.Is` checks continue to hold.

### Changed

- **`*ExFAT` is safe for concurrent use.** All directory-parse state moved off the
  handle into a parser created per parse, taking the struct from nineteen fields to
  three and a mutex. Two goroutines may read two directories, or two files, of one
  volume at once. A `*File` and the `io.ReadSeeker` from `File.Reader` hold cursors
  and remain single-goroutine; `File.ReaderAt` does not and is safe.

- **The library writes nothing to stdout.** `ShowAllEntriesInfo` and the
  entry-formatting helpers are gone; presentation belongs to the caller, and the
  `examples/` programs do their own. `ExtractAllFiles` no longer prints `Done!`.

- **`Entry.Name` no longer appends `" (deleted)"`.** The decoration made a name
  that could not be compared against the same file seen live, against another
  tool's output, or against an earlier reading of the same volume - and it landed
  in every path `Walk` composed. The library was already stripping the suffix back
  off internally to answer `HasNoName`, which is the tell: this was presentation
  living inside the parser, the same thing removing stdout addressed. `IsDeleted`
  is the flag to branch on, and a caller that wants the old rendering appends the
  string itself. The `DELETED` constant is gone with the behaviour.

- `examples/report` is a new example: the JSON document, or its counters with
  `-summary`, which prints the recorded fullness hint beside the count from the
  bitmap.

- `examples/list-all` classifies entries in the same five kinds a report row uses -
  region, virtual, metadata, directory, file - and marks a record whose
  `AllocationPossible` bit is clear. Its kind test previously matched special files
  before virtual ones, so `$MBR` and `$FAT1` were reported as metadata and the
  "virtual" label was unreachable.

- `Timestamps` carries JSON tags, so it can be embedded in a report row. A
  timestamp the volume does not record is omitted rather than written as a zero
  time; the offset-validity flags are always present, because they are provenance.

- `examples/list-all` is rebuilt on `WalkWithOptions` and reports each entry's
  `FileID`, replacing its own recursion and path composition. Its `-deleted` flag
  is the shortest demonstration of the deleted and recovered walk options.

- `ExtractAllFiles(rootEntries []Entry, dstdir string)` is now
  `ExtractAllFiles(ctx context.Context, dstdir string)`. It walks the tree itself,
  streams each file from its located extents, and reports the first damaged file
  after extracting everything else rather than stopping at it.

- `VolumeLabel() (string, error)` replaces `GetVolumeLabel() string`, and reads the
  root directory if that has not happened yet. The old form returned the empty
  string until something else happened to read the root, so the same call on the
  same volume gave two different answers depending on call order, and neither said
  it was incomplete.

- `ClusterOffset(cluster uint32) (uint64, error)` replaces
  `GetClusterOffset(cluster uint32) uint64`, and refuses a cluster the volume cannot
  address instead of returning an offset computed from it. For cluster 0 that offset
  landed in the FAT, or before the start of the volume.

- Accessors dropped their `Get` prefix and say what they return: `GetName` to
  `Name`, `GetRawName` to `RawName`, `GetSize` to `Size`, `GetValidDataSize` to
  `ValidDataSize`, `GetEntryCluster` to `FirstCluster`, `GetEntryType` to
  `EntryType`, `GetAttributes` to `Attributes`, `GetNameLength` to `NameLength`,
  `GetRegionOffset` to `RegionOffset`, `GetTimestamps` to `Timestamps`,
  `GetModifiedTime` / `GetCreatedTime` / `GetAccessedTime` to `ModifiedTime` /
  `CreatedTime` / `AccessedTime`, `GetClusterSize` to `ClusterSize`,
  `GetClusterList` to `ClusterList`, `GetAllocatedClusters` to `AllocatedClusters`,
  `GetFreeClusters` to `FreeClusters`, `GetAllEntries` to `AllEntries`.

- `DoesNotHaveFatChain` and `HasFatChain` collapse into one `IsContiguous`.
  **Check the sense when migrating**: `IsContiguous` is the old
  `DoesNotHaveFatChain`, so the old `HasFatChain` is `!IsContiguous`.

- `IsIndexable` becomes `IsContiguousFile` and `IsNotIndexable` is gone;
  `GetIndexableEntries` and `GetFullPathIndexableEntries` become `ContiguousFiles`
  and `ContiguousFilePaths`. The behaviour is unchanged, but the old names hid it:
  the filter excludes every entry with a FAT chain, which is to say every fragmented
  file on the volume. Use `Walk` to see the whole tree.

- `IsIndexed` becomes `IsInUse`, which is what it tests.

- `GetUsedSpace() string` becomes `PercentInUse() byte`. `GetDataLen` and
  `GetValidDataLen`, which returned formatted strings, are removed; formatting is
  the caller's.

- `Entry` grew from 104 to 112 bytes for the three identity fields. It is now
  exactly 112 bytes of fields with no padding, so any further field costs its own
  width and nothing more.

### Removed

- `ShowAllEntriesInfo`, and the unexported entry-formatting and size-humanising
  helpers behind it.
- `GetDataLen`, `GetValidDataLen`, `GetUsedSpace`, `GetVolumeLabel`,
  `GetClusterOffset`, `HasFatChain`, `IsNotIndexable`, `IsIndexed`.
- The `DELETED` constant, with the name decoration it existed for.
- `IMPROVEMENTS.md`, a historical summary whose remaining contents were either done
  or superseded, and the README section that recommended it as current.
- Dead internals: `Entry.readNameLen`, `ExFAT.clusterdata`, `readClusters`,
  `nextCluster`, and four write-only `VBR` fields including the misspelt
  `bitmcapCluster`.
