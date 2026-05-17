# ExFAT File System Parsing: High-Level Overview

## Introduction

This document provides a concise explanation of how the exFAT file system works and how the libxfat library parses it. Designed for developers who want to understand the architecture without overwhelming technical detail.

---

## What is ExFAT?

**ExFAT** (Extended FAT) is a modern file system optimized for removable media like USB drives, SD cards, and memory sticks. It supports files larger than 4GB and is universally compatible across Windows, macOS, Linux, cameras, and game consoles.

**Why it matters:**
- Simple but robust filesystem
- Perfect for forensic analysis and data recovery
- Designed for interoperability

---

## The Three-Part Architecture

### 1. Volume Boot Record (VBR)
The **"roadmap"** of the entire filesystem. Located at the start of the volume, it tells the parser:
- Where the FAT (allocation table) is stored
- Where the actual file data begins
- How large clusters are
- Total number of clusters
- Which cluster holds the root directory

Reading the VBR is the very first step; without it, you can't find anything else.

### 2. File Allocation Table (FAT)
A **linked-list manager** for fragmented files. Each cluster can point to the next cluster, creating chains:
- `Cluster 5 → Cluster 12 → Cluster 8 → END`

This allows files to be scattered across the disk without losing track of which clusters belong together.

### 3. Data Region + Directory Entries
Where the actual file data lives. Directories are special files containing 32-byte entries describing files and subdirectories.

---

## Core Concepts

### Clusters
Allocatable units of disk space (typically 4KB–32KB). Every file occupies whole clusters, even if it doesn't fill the last one.

**Cluster numbering in exFAT:**
- Starts at 2 (clusters 0 and 1 are reserved)
- Physical offset = dataAreaStart + ((clusterNumber - 2) × clusterSize)

### Directory Entries
Files are described by **multi-record** sets:
- **Primary Record**: Type=0x85 (file) or 0xA5 (directory), size, timestamps, first cluster
- **Stream Extension**: Flags, data length, contiguity info
- **Filename Record(s)**: Unicode filename

All three records work together as one logical "entry."

### FAT Chains
If a file doesn't fit in one cluster, the FAT tells us where the next piece is:

```
File starts at Cluster 5
→ FAT[5] = 12 (next cluster is 12)
→ FAT[12] = 18 (next cluster is 18)
→ FAT[18] = 0xFFFFFFFF (end of file)
```

The parser simply follows this chain to read complete files.

### Deleted Files
Files aren't actually deleted—just marked:
- Entry type byte has bit 7 cleared (0x85 → 0x05)
- File metadata remains on disk
- FAT entries remain intact
- Data blocks remain unchanged

This allows recovery of deleted files.

---

## How libxfat Works

### 1. Initialization
```go
exfat, _ := New(imageFile, optimistic)
// Parses VBR, calculates key offsets
```

The `optimistic` flag controls VBR validation strictness.

### 2. Reading the Root Directory
```go
entries, _ := exfat.ReadRootDir()
// Returns list of Entry objects
```

Reads the root directory (cluster specified in VBR) and parses all entries.

### 3. Navigating Directories
```go
subEntries, _ := exfat.ReadDir(entry)
// For any directory entry, returns its contents
```

Directories are just files containing entry records. Same parsing logic works recursively.

### 4. Extracting Files
```go
exfat.ExtractEntryContent(entry, outputPath)
// Writes file data to disk
```

For each file:
- Determine first cluster from entry
- If contiguous (NoFatChain=true): read sequentially
- If fragmented: follow FAT chain, assembling clusters in order
- Trim to exact file size

---

## Key Features

### Allocation Bitmap
ExFAT tracks free/allocated clusters via a bitmap file (`$BitMap`):
```go
allocated, _ := exfat.GetAllocatedClusters()
free, _ := exfat.GetFreeClusters()
```

Each bit represents one cluster: 1 = allocated, 0 = free.

### Contiguity Optimization
Some files are stored contiguously (no fragmentation) and marked with `NoFatChain=true`. The parser skips FAT lookups for these, improving performance.

### Special Metadata Files
Protected from normal operations:
- `$BitMap`: Allocation bitmap
- `$UpCase`: Character conversion table
- `$Volume GUID`: Volume identifier
- `$TexFAT`: TexFAT metadata (if present)
- `$ACT`: Access Control Table (if present)

### Fragmentation Detection
```go
clusters, fileSize, _ := exfat.GetClusterList(entry)
// Returns all clusters used by file
```

With this, you can analyze fragmentation patterns or understand how file data is distributed.

---

## Entry Structure

### File Entry (Type 0x85)
```
├─ Entry Type: 0x85 (file) or 0xA5 (directory)
├─ Timestamps: Created, Modified, Accessed
├─ Attributes: ReadOnly, Hidden, System, Directory, Archive
└─ Secondary Count: How many more records to read
```

### Stream Extension (Type 0xC0)
```
├─ First Cluster: Where data begins
├─ Data Length: File size in bytes
├─ Valid Data Length: Bytes actually written
└─ No FAT Chain Flag: Is file contiguous?
```

### Filename (Type 0xC1)
```
├─ Name Length: 1–15 characters
└─ Unicode Name: The actual filename
```

**Parsing Challenge**: These three records must be assembled into a complete entry, with checksum validation to ensure nothing is corrupted.

---

## Workflow Example

```
User calls: exfat.ReadRootDir()

1. Read VBR
   → Get rootDirCluster = 47

2. Get Root Directory Data
   → Follow FAT chain starting from cluster 47:
     FAT[47] = 48 → Read cluster 48
     FAT[48] = 0xFFFFFFFF → Done
   → Got directory content (32-byte records)

3. Parse Entries
   → Record 0 (0x85): File "myfile.txt"
   → Record 1 (0xC0): Stream info, size=1048576, cluster=100
   → Record 2 (0xC1): "myfile.txt" in Unicode
   → Create Entry object, add to list

4. Return to User
   → List of Entry objects, each representing a file/directory
```

---

## Reading Files

**For a small contiguous file:**
```
1. Get first cluster and size from entry
2. Calculate number of clusters needed
3. Read sequentially: dataStart + (clusters × clusterSize)
4. Trim to file size
5. Done!
```

**For a fragmented file:**
```
1. Get first cluster from entry
2. Loop: Read cluster, get next from FAT, repeat until 0xFFFFFFFF
3. Assembly all cluster data
4. Trim to file size
5. Done!
```

Both use the same interface from the user's perspective—the library handles the complexity.

---

## Deleted File Handling

The parser detects deleted files but prevents accidental misuse:

```go
entry.IsDeleted()  // true if marked as deleted
entry.IsValid()    // false if deleted or special file

// ExtractEntryContent() will refuse deleted files:
if entry.IsDeleted() {
    return ErrDeletedEntry
}
```

The data is still there and recoverable, but requires explicit intent.

---

## Error Handling

The parser gracefully handles:
- **Partial images**: Returns data available, stops on EOF
- **Corrupted entries**: Validates checksums, skips invalid records
- **Bad sectors**: Handled by attempted read with error propagation
- **Fragmented data**: Transparently follows FAT chain

```go
if err != nil {
    if errors.Is(err, io.EOF) || errors.Is(err, ErrEOF) {
        // Partial read - still usable
    } else {
        // Real error - handle accordingly
    }
}
```

---

## Performance Considerations

### FAT Chain vs. Contiguous
- **Contiguous** (NoFatChain=true): Direct sequential read, very fast
- **Fragmented**: One FAT lookup per cluster, slower but handles any layout

### Bitmap Counting
```go
exfat.GetAllocatedClusters()
// Counts bits in bitmap file
// O(bitmap size in bytes) complexity
```

### Directory Traversal
Recursive reading of subdirectories can be expensive for large directory trees. The library provides:
```go
exfat.GetAllEntries(rootEntries)  // Recursive
exfat.ReadDirs(entries)            // One level
```

---

## libxfat Design Philosophy

1. **Safety First**: Won't extract deleted files without explicit knowledge
2. **Defensive Parsing**: Validates checksums, checks buffer bounds
3. **Transparency**: Handles fragmentation without user intervention
4. **Flexibility**: Low-level cluster access available for forensics

---

## Typical Usage

```go
// Open filesystem
exfat, err := libxfat.New(imageFile, false)
if err != nil {
    log.Fatal(err)
}

// Read root directory
entries, _ := exfat.ReadRootDir()

// Process files
for _, entry := range entries {
    if entry.IsFile() && !entry.IsDeleted() {
        fmt.Printf("File: %s (%d bytes)\n",
                   entry.GetName(),
                   entry.GetSize())

        // Extract if needed
        exfat.ExtractEntryContent(entry, "/tmp/" + entry.GetName())
    }
}

// Analyze filesystem
allocated, _ := exfat.GetAllocatedClusters()
free, _ := exfat.GetFreeClusters()
fmt.Printf("%d clusters allocated, %d free\n", allocated, free)
```

---

## Summary

**ExFAT parsing in three steps:**

1. **Parse VBR** → Get the filesystem layout (where FAT, data, root dir are)
2. **Read Directories** → Navigate the tree, find files
3. **Follow FAT or Read Contiguously** → Get file data, handling fragmentation transparently

libxfat encapsulates this complexity into a clean API while maintaining safety and allowing forensic-grade access to filesystem details.

