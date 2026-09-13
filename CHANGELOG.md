# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [2.0.0] - unreleased

Version 2 changes the module path to `github.com/aoiflux/libxfat/v2`. A v1
consumer is unaffected until it opts in.

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

### Added

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
- Dead internals: `Entry.readNameLen`, `ExFAT.clusterdata`, `readClusters`,
  `nextCluster`, and four write-only `VBR` fields including the misspelt
  `bitmcapCluster`.
