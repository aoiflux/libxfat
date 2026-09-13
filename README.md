# libxfat

libxfat is a Go library for reading exFAT filesystem images. It is aimed at
forensics and inspection workflows: opening an image, parsing the VBR, walking
directories, reading metadata, and extracting file contents.

The library is read-oriented. It does not create or modify exFAT volumes.

## Highlights

- Parse exFAT images from an `*os.File` or any `io.ReaderAt`, so the library can
  be layered directly over a decoded EWF/VHD device or a partition reader.
- Walk the whole tree in one cancellable pass, with paths, parent identity and a
  cycle guard — and no filter hiding fragmented files.
- Map any file to absolute byte ranges without reading its content, with the
  provenance of every range reported rather than assumed.
- Read file content in memory through `io.ReaderAt`, `io.Reader` or
  `io.SectionReader`, clamped to the bytes actually located.
- Safe for concurrent use: one open volume, many goroutines.
- Extract regular files while preserving directory structure, plus the
  filesystem's own metadata streams and regions.
- Report full MACB timestamps in UTC, anchored by the exFAT UTC offset fields.
- Report volume statistics such as cluster size, used space, and allocation
  bitmap counts.
- Surface exFAT metadata entries such as `$BitMap`, `$UpCase`, `$Volume GUID`,
  `$TexFAT`, and `$ACT`.
- Add virtual metadata entries such as `$MBR`, `$FAT1`, and `$FAT2` to the root
  listing.
- Handle truncated and malformed images more defensively than the original
  implementation.

## Install

```sh
go get github.com/aoiflux/libxfat/v2
```

```go
import "github.com/aoiflux/libxfat/v2"
```

</div>
<br>

---

## Table of Contents

- [Table of Contents](#table-of-contents)
- [Overview](#overview)
- [Features](#features)
- [Project Structure](#project-structure)
  - [Project Index](#project-index)
- [Internal Parsing Notes](#internal-parsing-notes)
- [Getting Started](#getting-started)
  - [Prerequisites](#prerequisites)
  - [Installation](#installation)
- [Contributing](#contributing)
- [Cite Paper](#cite)
- [Read Paper](#paper)

---

## Overview

libxfat is a Go library offering a robust and efficient solution for parsing and
manipulating ExFAT filesystems. It provides comprehensive tools for extracting
data and accessing metadata.

**Why libxfat?**

This project simplifies ExFAT filesystem interaction for developers. The core
features include:

- **🟢 Robust ExFAT Parsing:** Handles both contiguous and chained cluster
  allocation schemes for reliable data extraction.
- **🔵 Comprehensive Metadata Access:** Easily retrieve file size, attributes,
  timestamps, and generate directory listings.
- **🟡 Efficient Data Extraction:** Optimized for speed and performance when
  working with large ExFAT volumes.
- **🔴 Clear Data Structures:** Well-defined structs (VBR, Entry) simplify ExFAT
  data manipulation and understanding.
- **🟣 Thorough Error Handling:** Includes integrity checks and robust error
  handling to prevent data loss.
- **🟠 Well-Documented Code:** Clean, well-commented code ensures easy
  integration and maintainability.

## Internal Parsing Notes

The internal parser architecture and zero-copy boundaries are documented in
[INTERNAL_PARSING.md](./INTERNAL_PARSING.md).

---

## Features

|    | Component         | Details                                                                                                                                                                                |
| :- | :---------------- | :------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| ⚙️ | **Architecture**  | <ul><li>Pure Go implementation</li><li>Modular design with separate packages for different FAT functionalities</li></ul>                                                               |
| 🔩 | **Code Quality**  | <ul><li>Generally well-structured code</li><li>Uses descriptive variable and function names</li><li>Some areas could benefit from more comments</li></ul>                              |
| 📄 | **Documentation** | <ul><li>Limited documentation</li><li>Relies heavily on code comments for explanation</li><li>No formal API documentation</li></ul>                                                    |
| 🔌 | **Integrations**  | <ul><li>Designed to be easily integrated into other Go projects</li><li>No external library dependencies beyond Go's standard library</li></ul>                                        |
| 🧩 | **Modularity**    | <ul><li>Good modularity with distinct packages for file system operations, directory handling, etc.</li><li>Facilitates independent testing and maintainability</li></ul>              |
| ⚡️ | **Performance**   | <ul><li>Performance not explicitly optimized in the code</li><li>Further analysis required to determine performance characteristics</li></ul>                                          |
| 🛡️ | **Security**      | <ul><li>No explicit security measures implemented (e.g., input validation)</li><li>Security considerations need to be addressed for production use</li></ul>                           |
| 📦 | **Dependencies**  | <ul><li>Only relies on the Go standard library</li><li>No external dependencies, reducing complexity and potential conflicts</li></ul>                                                 |
| 🚀 | **Scalability**   | <ul><li>Scalability depends on the application using the library</li><li>The library itself is not inherently limited in scalability</li><li>Tested with datasets up to 1TiB</li></ul> |

---

## Project Structure

```sh
└── libxfat/
    ├── README.md
    ├── CHANGELOG.md
    ├── xfat.go          # package docs, Source, constructors
    ├── chain.go         # the topology-only FAT walk
    ├── cluster.go       # cluster traversal and content reads
    ├── const.go
    ├── context.go       # cancellation pacing
    ├── dir.go           # directory parsing entry points, extraction
    ├── dir_record.go
    ├── entry.go
    ├── extent_reader.go # io.ReaderAt over a file's located ranges
    ├── file.go          # OpenEntry and *File
    ├── fragment.go      # Range, FragmentResult, the extent API
    ├── identity.go      # FileID and entry addressing
    ├── parser.go        # per-parse directory parser state
    ├── reader.go
    ├── struct.go
    ├── timestamp.go
    ├── upcase.go
    ├── util.go
    ├── validators.go
    ├── vbr.go
    ├── volume.go        # volume identity and geometry accessors
    └── walk.go          # Walk and WalkOptions
```

The module currently targets Go 1.25 as declared in `go.mod`.

Version 2 changed the module path, so the import is
`github.com/aoiflux/libxfat/v2`. See [CHANGELOG.md](CHANGELOG.md) for what moved
and why.

## Quick Start

```go
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/aoiflux/libxfat/v2"
)

func main() {
	imageFile, err := os.Open("disk.exfat")
	if err != nil {
		log.Fatal(err)
	}
	defer imageFile.Close()

	fs, err := libxfat.New(imageFile, false)
	if err != nil {
		log.Fatal(err)
	}

	rootEntries, err := fs.ReadRootDir()
	if err != nil {
		log.Fatal(err)
	}

	for _, entry := range rootEntries {
		fmt.Printf("name=%q size=%d dir=%t special=%t virtual=%t\n",
			entry.Name(),
			entry.Size(),
			entry.IsDir(),
			entry.IsSpecialFile(),
			entry.IsVirtualEntry(),
		)
	}

	allocated, err := fs.AllocatedClusters()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("cluster size: %d bytes\n", fs.ClusterSize())
	fmt.Printf("allocated clusters: %d\n", allocated)
	fmt.Printf("used space: %d%%\n", fs.PercentInUse())
}
```

The second argument to `libxfat.New` is `optimistic`:

- `false`: strict mode, preferred for forensic use.
- `true`: skip strict VBR offset verification when working with less reliable
  images or embedded volumes.

You can also pass an optional sector offset if the exFAT filesystem starts
inside a larger image:

```go
fs, err := libxfat.New(imageFile, false, 2048)
```

### Opening Without A File

`NewFromReaderAt` accepts any `io.ReaderAt`, so a volume can be parsed straight
out of a decoded container (EWF, VHD, ...) or an in-memory buffer with no
temporary file in between:

```go
fs, err := libxfat.NewFromReaderAt(device, deviceSize, false, 2048)
```

`size` may be `0` if unknown, but supplying it is worthwhile: it turns a read
past the end of a truncated or malformed image into a clean
`io.ErrUnexpectedEOF` instead of deferring to whatever the reader does with an
out-of-range offset.

Reads no longer move a shared seek cursor, so several independently opened
volumes may share one reader concurrently. A single `ExFAT` value is still not
safe for concurrent use — directory parsing keeps mutable state on it — so open
one per goroutine.

### Opening A Partition

Strict mode cross-checks the `PartitionOffset` recorded in the volume boot
record against where the volume was opened. That works for a whole-disk image,
but not for a reader already scoped to a partition: the reader starts at byte 0
while the volume still records its true LBA, and the two disagree.

`Open` separates the two facts:

```go
partition := io.NewSectionReader(disk, partitionStart, partitionLength)

fs, err := libxfat.Open(libxfat.Source{
    Reader:       partition,
    Size:         partitionLength,
    Strict:       true,
    PartitionLBA: 2048, // where the partition table says this volume lives
})
```

Set `IgnorePartitionOffset` instead when the LBA is unknown. Many imaging tools
and virtual disk formats write a zero `PartitionOffset` regardless of where the
volume actually sits, so this is a legitimate setting rather than an escape
hatch — but it does discard one consistency signal.

### Name Checksum Verification

The other half of strict mode is verifying each directory entry set against the
`EntrySetChecksum` recorded in its primary record. A mismatch means the set was
damaged after it was written — a partial overwrite, a torn write, a record
reused by a later file.

A mismatch never costs you the name. The bytes on disk are what they are, and a
name parsed out of a damaged set is still the only record of what the file was
called, so it is always returned and the verdict travels alongside it:

```go
for _, entry := range entries {
    if err := entry.NameChecksumError(); err != nil {
        log.Printf("unverified: %v", err)   // entry.Name() is still populated
    }
}
```

- `NameChecksumVerified()` — checked and matched.
- `NameChecksumMismatch()` — checked and disagreed.
- `NameChecksumError()` — the mismatch as an `error` wrapping
  `ErrNameChecksumMismatch`, or `nil`.
- `EntrySetChecksums()` — the recorded and computed values, plus whether the
  comparison ran at all.

Both predicates are false in optimistic mode, which keeps *not checked*
distinguishable from *checked and passed*: a report cannot claim integrity it
never tested.

Set `Source.RejectChecksumMismatch` to drop mismatched entry sets outright. It
only applies alongside `Strict`, and it is off by default — for most evidence
work a damaged entry is more interesting than a missing one.

### Name Hash Verification

Alongside the entry set checksum, each file entry set records a hash of its own
up-cased name. The two are independent statements about the same bytes, and they
can disagree — a name rewritten in place with the checksum recomputed but the
hash left stale passes one and fails the other. For evidence work, that
disagreement is the interesting part:

```go
switch err := fs.VerifyNameHash(entry); {
case errors.Is(err, libxfat.ErrNoNameHash):
    // Not a file entry set — $MBR, $BitMap and friends record no hash.
case errors.Is(err, libxfat.ErrNameHashMismatch):
    log.Printf("name does not match its recorded hash: %v", err)
case err != nil:
    log.Printf("could not check: %v", err)   // e.g. ErrUpcaseTableNotFound
}
```

Hashing is defined in terms of the volume's own up-case table, not Unicode's
rules or `strings.ToUpper` — the hash was computed with that table, so anything
else yields mismatches that mean nothing. The table is read from the volume on
demand and exposed directly, since "equal names" in exFAT means whatever the
volume says it means:

- `UpcaseString(s string) (string, error)` — fold through the volume's table.
- `NameHash(name string) (uint16, error)` — the hash the volume would record.
- `Entry.RecordedNameHash() (uint16, bool)` — the stored value.
- `Entry.RawName()` — the name as recorded, without the deleted marker, the
  placeholder for a nameless entry, or any composed path.

### Entries With No Name

An entry set whose name records decode to nothing — absent, or all NUL — leaves
the entry with no name at all. That is worse than it sounds: traversal treats a
nameless directory as unreadable, so everything beneath it drops out of a walk
without an error.

Entries are located by cluster, not by name, so nothing has to be lost. Such an
entry is given a placeholder keyed to its first cluster, `$Unnamed-<cluster>`,
which is stable between runs and distinct between siblings, and it reports
`HasSyntheticName()`:

```go
if entry.HasSyntheticName() {
    // Name() is the library's invention, not a name off the volume.
}
```

Check it anywhere a name reaches a person or a report. The placeholder keeps the
subtree addressable; it is not evidence.

## Core API

### Open And Inspect

- `New(imagefile *os.File, optimistic bool, offset ...uint64) (*ExFAT, error)`
- `NewFromReaderAt(r io.ReaderAt, size int64, optimistic bool, offset ...uint64) (*ExFAT, error)`
- `Open(src Source) (*ExFAT, error)`
- `ReadRootDir() ([]Entry, error)`
- `ReadDir(entry Entry) ([]Entry, error)`
- `ReadDirs(entries []Entry) ([]Entry, error)`
- `AllEntries(rootEntries []Entry, indexable ...bool) ([]Entry, error)`
- `ContiguousFiles(rootEntries []Entry) ([]Entry, error)`
- `ContiguousFilePaths(entries []Entry, path string) ([]Entry, error)`

`ContiguousFiles` and `ContiguousFilePaths` apply the same test to the same tree,
so they select the same entries and differ only in whether paths are composed.
The synthetic `$MBR` and `$FAT1` appear in both, at the root.

Read what that test excludes before choosing either: as its name now says, it
keeps only *contiguous* files, so **every fragmented file on the volume is
absent**. Use `Walk` for anything that needs to see the whole tree.

### Walk The Tree

- `Walk(ctx context.Context, fn func(path string, parentFirstCluster uint32, entry Entry) error) error`
- `WalkWithOptions(ctx context.Context, opts WalkOptions, fn ...) error`

One pre-order, depth-first pass in disk order, with a cycle guard, a depth cap and
a cancellable context. The path is handed to the callback rather than written into
the entry, so `Entry.Name()` stays the bare name at every depth. Nothing is
filtered on fragmentation. `WalkOptions` opts into deleted records
(`IncludeDeleted`), into the surviving children of a deleted directory
(`DescendDeletedDirectories`), and into the free-space sweep
(`IncludeRecovered`), which reports its carvings under `RecoveredPath`.

### Read File Content

- `OpenEntry(entry Entry) (*File, error)`
- `ReadEntry(entry Entry) ([]byte, error)`
- `File`: `Read`, `ReadAt`, `ReadAll`, `WriteTo`, `Seek`, `Reader`, `ReaderAt`,
  `SectionReader`, `Size`, `ValidSize`, `Fragments`, `Slack`, `Unwritten`

Reads are clamped to the bytes actually *located*, never to the size the
directory entry records, so a truncated or broken chain yields what is there and
says so rather than padding.

### Map File Extents

- `FragmentOffsets(entry Entry) ([]Range, error)`
- `FragmentOffsetsWithOptions(entry Entry, opts FragmentOptions) (*FragmentResult, error)`
- `IsFragmented(entry Entry) (bool, error)`
- `SlackRange(entry Entry) (Range, bool, error)`
- `UnwrittenRanges(entry Entry) ([]Range, error)`
- `IsClusterAllocated(cluster uint32) (bool, error)`
- Free functions: `TotalLength([]Range)`, `IsFragmented([]Range)`, `Coalesce([]Range)`

`Range` gives absolute byte offsets and lengths, coalesced into runs, so a
changed-range list can be intersected against a file's extents without reading
any file data. `FragmentResult` carries the provenance: `ChainWalked`,
`NoFatChain`, `Assumed`, `Truncated`, `ChainBroken`, `LoopDetected`,
`FirstClusterReallocated`, `ValidBytes`.

### Identify Entries

- `FileID(entry Entry) (FileID, bool)`
- `Entry.EntrySetOffset() (int64, bool)`
- `Entry.ParentFirstCluster()` and `Entry.EntrySlotIndex()`

`FileID` is a parent cluster plus a logical slot index: stable across the parent
directory being relocated, and explicitly best-effort. Read its documentation
before using it for rename detection — a reused slot carries its predecessor's
identity exactly, and no field on an exFAT volume distinguishes the two.

### Extract Data

- `ExtractEntryContent(entry Entry, dstpath string) error`
- `ExtractAllFiles(ctx context.Context, dstdir string) error`

`ExtractAllFiles` walks the tree itself and writes nothing to stdout. A name that
would resolve outside the destination directory is refused rather than followed.

### Deleted Entry Recovery

- `RecoverDeletedEntries() ([]Entry, error)`
- `RecoverDeletedEntriesContext(ctx context.Context) ([]Entry, error)`

### Volume Statistics

- `VolumeLabel() (string, error)`
- `UpcaseString(s string) (string, error)`
- `NameHash(name string) (uint16, error)`
- `VerifyNameHash(entry Entry) error`
- `ClusterSize() uint64`
- `AllocatedClusters() (uint32, error)`
- `FreeClusters() (uint32, error)`
- `PercentInUse() byte`
- `CountClusters(entry Entry) (int, error)`
- `ClusterList(entry Entry) ([]uint32, uint64, error)`
- `ClusterOffset(cluster uint32) (uint64, error)`

`ClusterList` describes entries that live in the cluster heap. The synthetic
`$MBR`, `$FAT1` and `$FAT2` entries do not, so it returns `ErrNoClusterMapping`
for them; locate those with `entry.RegionOffset()` plus `entry.Size()`, or with
`FragmentOffsets`, which accepts them and returns the one range they occupy.

### Volume Identity And Geometry

- `VolumeSerialNumber() uint32`, `FilesystemRevision() (major, minor byte)`
- `VolumeFlags() uint16`, `ActiveFAT() int`, `VolumeDirty() bool`, `MediaFailure() bool`
- `Base() int64`, `PartitionOffset() uint64`, `VolumeSize() uint64`
- `BytesPerSector() uint32`, `SectorsPerCluster() uint32`, `ClusterCount() uint32`
- `RootDirCluster() uint32`, `ClusterHeapOffset() int64`
- `FatOffset() int64`, `FatSize() uint64`, `FatCount() byte`

`VolumeDirty` is the one to read first on an acquired image: it says the volume
was not cleanly unmounted, and so that its bitmap, directory records and FAT need
not agree with each other.

### Entry Helpers

Each parsed directory item is represented by `Entry`. Common helpers include:

- `Name()`
- `Size()`
- `ValidDataSize()`
- `FirstCluster()`
- `EntryType()`
- `Attributes()`
- `IsDir()` and `IsFile()`
- `IsDeleted()`
- `IsInUse()`
- `IsSpecialFile()`
- `IsVirtualEntry()`
- `IsRegion()` and `IsMetadataStream()`
- `RegionOffset() (uint64, bool)`
- `IsContiguous()` — the volume recorded `NoFatChain` for this stream
- `IsContiguousFile()` — the filter `ContiguousFiles` applies
- `NameChecksumVerified()` and `NameChecksumMismatch()`
- `NameChecksumError() error`
- `EntrySetChecksums() (expected, computed uint16, checked bool)`
- `HasSyntheticName()`
- `RawName()`
- `RecordedNameHash() (uint16, bool)`

### Timestamps

- `ModifiedTime() time.Time`
- `CreatedTime() time.Time`
- `AccessedTime() time.Time`
- `Timestamps() Timestamps`

All three getters return UTC, or the zero `time.Time` when the volume records
no usable value — check `IsZero()` rather than assuming a real date.

Two properties of exFAT are worth knowing before relying on these:

**There are three timestamps, not four.** exFAT stores creation, last
modification and last access. There is no Unix-`ctime` equivalent, so in MACB
terms an entry supplies M, A and C, with B (birth) being the same value as
creation. Creation and modification carry an extra 10ms-resolution field;
access does not, so access times always land on a two-second boundary.

**Stored times are wall-clock readings, not instants.** Each timestamp has a
companion UTC offset byte, and only that byte makes the reading absolute. When
a volume records no offset the getters return the stored wall clock as though it
were UTC, which may be wrong by the writing system's time zone. `Timestamps`
reports which case applies, and also returns the readings exactly as stored:

```go
ts := entry.Timestamps()
if ts.ModifiedOffsetValid {
    fmt.Println("anchored:", ts.Modified)          // true UTC
} else {
    fmt.Println("unanchored:", ts.ModifiedLocal)   // wall clock, zone unknown
}
```

## Special And Virtual Entries

`ReadRootDir()` returns both normal filesystem entries and metadata entries.

Special entries are real exFAT metadata records found in the image, including:

- `$BitMap`
- `$UpCase`
- `$Volume GUID`
- `$TexFAT`
- `$ACT`

Virtual entries are synthetic helpers added by the library to make filesystem
metadata easier to inspect from the root listing, including:

- `$MBR`
- `$FAT1`
- `$FAT2`

Use `entry.IsSpecialFile()` and `entry.IsVirtualEntry()` to distinguish them
from regular files and directories.

`$FAT2` is emitted only when the volume boot record declares two FATs, as TexFAT
volumes do; comparing the two tables is itself an evidentiary signal.

These entries are readable as well as listable. `ExtractEntryContent` accepts
the cluster-backed metadata streams (`$BitMap`, `$UpCase`) and the region-backed
synthetic entries (`$MBR`, `$FAT1`, `$FAT2`) alongside regular files:

```go
for _, entry := range entries {
    if entry.IsMetadataStream() || entry.IsRegion() {
        err := fs.ExtractEntryContent(entry, filepath.Join(outdir, entry.Name()))
        // ...
    }
}
```

## Examples

Runnable examples live under `examples/`.

```bash
go run ./examples/list-root -image /path/to/volume.exfat
go run ./examples/list-all -image /path/to/volume.exfat
go run ./examples/volume-stats -image /path/to/volume.exfat
go run ./examples/extract-all -image /path/to/volume.exfat -out ./recovered
```

Common flags:

- `-image`: path to the exFAT image file.
- `-optimistic`: skip strict VBR offset verification.
- `-offset`: sector offset where the exFAT volume begins.

The example programs cover:

- Listing root directory entries, including metadata and virtual entries.
- Walking the full filesystem and printing full paths for indexable entries.
- Reporting volume and allocation statistics.
- Extracting all regular files into an output directory.

## Testing

Run the full test suite with:

```bash
go test ./...
```

The repository includes both package-level tests and higher-level tests under
`tests/` that exercise:

- VBR validation.
- FAT loop and EOF-range handling.
- Root directory parsing.
- Virtual and special entry behavior.
- Allocation bitmap counting.
- Path-preserving extraction behavior.
- Equivalence between the `*os.File` and `io.ReaderAt` paths.
- Partition-relative and 4096-byte-sector volumes.
- Timestamp decoding, including UTC offsets and malformed dates.

Concurrency is worth checking too, since several volumes may share one reader:

```bash
go test -race ./...
```

### Fuzzing

Three targets cover the parsers that walk attacker-controlled lengths and counts:

```bash
go test -fuzz FuzzParseVBRData -fuzztime 60s .
go test -fuzz FuzzParseDirChunk -fuzztime 60s .
go test -fuzz FuzzOpen -fuzztime 60s .
```

### Real Images

The synthetic image the unit tests build is deliberately tiny: a single-cluster
root directory, no subdirectories, no fragmented files. That leaves FAT chain
walking, multi-cluster directories and fragmented extraction untested against
anything a real formatter produced.

Point `LIBXFAT_CORPUS` at a directory of exFAT images to close that gap without
committing binary fixtures:

```bash
LIBXFAT_CORPUS=/path/to/images go test ./tests/ -run Corpus -v
```

Images may be raw volume dumps or whole-disk images with a partition table; the
harness locates the volume boot record either way. It checks that fragments land
inside the image, that extracted content is exactly the advertised length, that
valid data length never exceeds allocated length, and that timestamps are either
absent or plausible. Volumes formatted by Windows, by macOS and by `mkfs.exfat`
are all worth including, since the three disagree about the number of FATs, the
sector size, and whether a UTC offset is recorded at all.

## Notes On Robustness

Recent parser improvements in this repository include:

- Better bounds checking when reading cluster-backed records.
- Safer UTF-16 filename decoding.
- Directory-set checksum validation.
- Validation helpers for key exFAT directory record types.
- More reliable handling of short bitmaps, FAT loops, and truncated images.

See `IMPROVEMENTS.md` for a more detailed implementation summary.

## Repository Layout

```text
.
|-- xfat.go           # entry point: package docs, Source, and the constructors
|-- walk.go           # Walk, WalkWithOptions, WalkOptions
|-- context.go        # how cancellation is paced across an operation
|-- dir.go            # directory parse entry points, extraction, deleted carving
|-- parser.go         # dirParser: the state of one directory parse
|-- fragment.go       # Range, FragmentResult, and the extent API
|-- chain.go          # the FAT walk that reads only FAT entries
|-- file.go           # OpenEntry and *File
|-- extent_reader.go  # io.ReaderAt and io.Reader over a file's ranges
|-- identity.go       # FileID, and an entry's own address
|-- volume.go         # the volume's identity and geometry
|-- vbr.go            # VBR parsing
|-- cluster.go        # cluster traversal and content reads
|-- reader.go         # bounds-checked reads against the backing io.ReaderAt
|-- dir_record.go     # 32-byte directory record view
|-- entry.go          # the contiguous-file filter
|-- upcase.go         # up-case table and name hashing
|-- timestamp.go      # exFAT timestamp decoding
|-- struct.go         # core ExFAT, VBR, and Entry types
|-- util.go           # shared parsing helpers
|-- validators.go     # exFAT directory-record validation helpers
|-- examples/         # runnable example programs
`-- tests/            # higher-level behavioral tests
```

## Contributing

- **🐛 [Report Issues](https://github.com/aoiflux/libxfat/issues)**: Submit bugs
  found or log feature requests for the `libxfat` project.
- **💡
  [Submit Pull Requests](https://github.com/aoiflux/libxfat/blob/main/CONTRIBUTING.md)**:
  Review open PRs, and submit your own PRs.

Issues and pull requests are welcome. If you change parsing behavior, prefer
adding or updating tests in the same change so malformed-image handling and
metadata behavior remain covered.

## Citation

Gogia, G., & Rughani, P. (2024). Parex: A novel exfat parser for file system
forensics. Computación y Sistemas, 28(2). https://doi.org/10.13053/cys-28-2-4804

## Paper

## [PAREX: A Novel exFAT Parser for File System Forensics](https://www.scielo.org.mx/scielo.php?script=sci_arttext&pid=S1405-55462024000200421#:~:text=This%20research%20proposes%20a%20novel%20open-source%20exFAT%20file,of%20disk%20images%20ranging%20from%201MiB%20to%201TiB)
