# Internal Parsing Architecture

## Scope

This document describes the current internal parsing direction for libxfat after
the incremental refactor toward a zero-copy-oriented design. The public `ExFAT`,
`VBR`, and `Entry` APIs remain stable. The changes are internal and focus on
reducing avoidable allocations while keeping parsing deterministic and resilient
on malformed images.

## Ownership Expectations

- Cluster buffers passed through `visitFatChain`, `visitContiguousClusters`, and
  `visitEntryData` are borrowed scratch buffers owned by `VBR` traversal code
  for the duration of the callback only.
- `dirRecordView` is a borrowed 32-byte view into the current cluster chunk. It
  must never be stored outside the current parse step.
- Parsed `Entry` values are owned results. Scalar metadata is copied out of
  borrowed records into the `Entry` struct before the next cluster buffer reuse.
- File names and volume labels become owned Go strings. This copy is intentional
  because Go strings cannot safely alias the reusable scratch buffers.
- `readContent` still returns an owned `[]byte` because the public API promises
  a materialized content buffer. The implementation now allocates exactly
  `entry.dataLen` bytes instead of assembling extra whole-cluster tails.

## Borrowed Versus Owned Structures

Borrowed internal structures:

- `dirRecordView`
- Cluster visitor callback slices in `cluster.go`
- Bitmap chunks consumed by `bitmapCounter`
- Per-chunk directory parser state in `parseDirChunk`

Owned result structures:

- `Entry`
- `VBR.volumeLabel`
- The `[]byte` returned from `readContent`
- The cluster slice returned from `GetClusterList`

This split keeps exFAT internals decoupled from upward-facing VFS abstractions.
Callers still receive stable owned values, while the parser does most internal
work directly against borrowed byte slices.

## Zero-Copy Boundaries

The current zero-copy boundaries are:

- Directory entry parsing reads each 32-byte record through `dirRecordView`
  without copying the record body.
- Root directory and subdirectory parsing stream cluster-by-cluster rather than
  loading the full directory body into memory first.
- FAT traversal reuses a single cluster-sized scratch buffer while walking
  chained allocations.
- Allocation bitmap counting streams through `bitmapCounter` and no longer loads
  the full bitmap into memory.
- FAT-chained extraction writes chunk-by-chunk directly to the destination file.

Intentional copy boundaries remain at:

- UTF-16 name assembly into owned Go strings
- Volume label decoding into an owned Go string
- `readContent`, because its return type is an owned byte slice
- `GetClusterList`, because its return type is an owned cluster list

## Malformed-Image Resilience

The refactor preserves stream-oriented access and keeps the parser defensive:

- Cluster reads now use full reads for deterministic short-read handling.
- FAT walks stop with an error if a chain exceeds the declared cluster count,
  preventing infinite loops on corrupted images.
- Directory parsing still validates record types and checksum state before
  accepting file metadata.
- Deleted and unallocated metadata handling remains intact because the parser
  still recognizes deleted FILE, STREAM, and FILENAME record sets and emits
  owned `Entry` results only after set validation.

## Allocation Hotspots That Remain

The main remaining allocations are intentional or tied to public API shape:

- `readContent` allocates `entry.dataLen` bytes for returned content.
- Filename assembly allocates UTF-16 unit storage and the final UTF-8 string.
- `GetClusterList` allocates a slice of cluster numbers for callers that need a
  full list.
- User-facing formatting helpers in `entry.go` allocate display strings.

These are the main candidates for further reduction if new internal-only helpers
are added in the future. They should not be removed by changing existing public
method signatures.

## Guidance For Future Changes

- Prefer adding internal streaming helpers before changing any public API.
- Keep borrowed slices callback-scoped and synchronous.
- Copy data only when crossing an ownership boundary into exported or
  caller-retained structures.
- Preserve deterministic traversal order and validation behavior on malformed
  images.
- Preserve deleted-entry support when refactoring directory-set state machines.
