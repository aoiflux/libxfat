# libxfat

libxfat is a Go library for reading exFAT filesystem images. It is aimed at
forensics and inspection workflows: opening an image, parsing the VBR, walking
directories, reading metadata, and extracting file contents.

The library is read-oriented. It does not create or modify exFAT volumes.

## Highlights

- Parse exFAT images directly from an `*os.File`.
- Read the root directory or recursively walk indexable entries.
- Extract regular files while preserving directory structure.
- Report volume statistics such as cluster size, used space, and allocation
  bitmap counts.
- Surface exFAT metadata entries such as `$BitMap`, `$UpCase`, `$Volume GUID`,
  `$TexFAT`, and `$ACT`.
- Add virtual metadata entries such as `$MBR`, `$FAT1`, and `$FAT2` to the root
  listing.
- Handle truncated and malformed images more defensively than the original
  implementation.

## Install

```bash
go get github.com/aoiflux/libxfat
```

The module currently targets Go 1.25 as declared in `go.mod`.

## Quick Start

```go
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/aoiflux/libxfat"
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
			entry.GetName(),
			entry.GetSize(),
			entry.IsDir(),
			entry.IsSpecialFile(),
			entry.IsVirtualEntry(),
		)
	}

	allocated, err := fs.GetAllocatedClusters()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("cluster size: %d bytes\n", fs.GetClusterSize())
	fmt.Printf("allocated clusters: %d\n", allocated)
	fmt.Printf("used space: %s\n", fs.GetUsedSpace())
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

## Core API

### Open And Inspect

- `New(imagefile *os.File, optimistic bool, offset ...uint64) (ExFAT, error)`
- `ReadRootDir() ([]Entry, error)`
- `ReadDir(entry Entry) ([]Entry, error)`
- `ReadDirs(entries []Entry) ([]Entry, error)`
- `GetAllEntries(rootEntries []Entry, indexable ...bool) ([]Entry, error)`
- `GetFullPathIndexableEntries(entries []Entry, path string) ([]Entry, error)`

### Extract Data

- `ExtractEntryContent(entry Entry, dstpath string) error`
- `ExtractAllFiles(rootEntries []Entry, dstdir string) error`

### Deleted Entry Recovery

- `RecoverDeletedEntries() ([]Entry, error)`

### Volume Statistics

- `GetVolumeLabel() string`
- `GetClusterSize() uint64`
- `GetAllocatedClusters() (uint32, error)`
- `GetFreeClusters() (uint32, error)`
- `GetUsedSpace() string`
- `CountClusters(entry Entry) (int, error)`
- `GetClusterList(entry Entry) ([]uint32, uint64, error)`
- `GetClusterOffset(cluster uint32) uint64`

### Entry Helpers

Each parsed directory item is represented by `Entry`. Common helpers include:

- `GetName()`
- `GetSize()`
- `GetEntryCluster()`
- `IsDir()` and `IsFile()`
- `IsDeleted()`
- `IsIndexed()`
- `IsSpecialFile()`
- `IsVirtualEntry()`
- `HasFatChain()` and `DoesNotHaveFatChain()`

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
|-- exfat.go          # high-level filesystem operations
|-- vbr.go            # VBR parsing and volume metadata
|-- cluster.go        # cluster traversal and content reads
|-- entry.go          # directory-entry formatting helpers
|-- struct.go         # core ExFAT, VBR, and Entry types
|-- util.go           # shared parsing and formatting helpers
|-- validators.go     # exFAT directory-record validation helpers
|-- examples/         # runnable example programs
`-- tests/            # higher-level behavioral tests
```

## Contributing

Issues and pull requests are welcome. If you change parsing behavior, prefer
adding or updating tests in the same change so malformed-image handling and
metadata behavior remain covered.

## Citation

Gogia, G., & Rughani, P. (2024). Parex: A novel exfat parser for file system
forensics. Computacion y Sistemas, 28(2). https://doi.org/10.13053/cys-28-2-4804

Paper:
https://www.scielo.org.mx/scielo.php?script=sci_arttext&pid=S1405-55462024000200421
