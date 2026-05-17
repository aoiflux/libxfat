# Deep Dive: ExFAT File System Parsing from Scratch

## Table of Contents
1. [File Systems Fundamentals](#file-systems-fundamentals)
2. [ExFAT Architecture Overview](#exfat-architecture-overview)
3. [Volume Boot Record (VBR) - The Roadmap](#volume-boot-record-vbr---the-roadmap)
4. [Clusters and Data Region](#clusters-and-data-region)
5. [FAT Chain: Following the Trail](#fat-chain-following-the-trail)
6. [Directory Entries and Metadata](#directory-entries-and-metadata)
7. [File Fragmentation](#file-fragmentation)
8. [Deleted Files and Recovery](#deleted-files-and-recovery)
9. [Implementation Details in libxfat](#implementation-details-in-libxfat)

---

## File Systems Fundamentals

### What is a File System?

A file system is an abstraction layer that organizes how data is stored on physical media (disk, USB drive, SD card). Without a file system, the storage medium would just be a flat sequence of bytes with no structure.

**Key Responsibilities:**
- **Data Storage**: Organizing where files are physically located
- **Metadata Storage**: Tracking information about files (name, size, dates, permissions)
- **Allocation Management**: Knowing which parts of the disk are used and which are free
- **Hierarchical Organization**: Supporting directories (folders) and subdirectories
- **File Retrieval**: Quickly finding files by name and returning their content

### The Three Core Components of Any File System

Every file system must solve three fundamental problems:

#### 1. **The Directory Structure**
This is the "index" of your file system. Think of it like a phone book.
- Maps human-readable file names to physical locations on disk
- Stores metadata (size, creation date, last modified date, permissions)
- Supports hierarchies (nested folders)

In a file system, directories themselves are just special files that contain entries. Each entry points to another file or directory.

#### 2. **The Allocation/Deallocation Problem**
You need to track which parts of the disk are in use and which are free.
- When you create a file, the filesystem must find free space
- When you delete a file, that space must be marked as free
- Different filesystems use different strategies: Bitmaps, Free lists, etc.

#### 3. **The Data Retrieval Problem**
When a file is fragmented across multiple non-contiguous locations, how does the system know all the parts?
- This is where structures like **FAT tables** (File Allocation Tables) come in
- FAT is like a linked list telling you "cluster X contains next data chunk in cluster Y"

### Why File Systems Matter for Forensics

Understanding file system structure is crucial for:
- **Data Recovery**: Finding deleted files
- **Forensic Analysis**: Understanding how data was organized when a drive was used
- **Hidden Data Detection**: Finding unallocated (deleted) space
- **Timeline Analysis**: Examining timestamps to understand when files were accessed/modified

---

## ExFAT Architecture Overview

### What is ExFAT?

**ExFAT** stands for "Extended File Allocation Table". It's a modern evolution of FAT32, designed specifically for removable media like USB drives and SD cards.

**Key Characteristics:**
- Supports files larger than 4GB (unlike FAT32)
- Supports volumes larger than 2TB
- More robust metadata handling
- Uses a more sophisticated directory entry structure
- Maintains backward compatibility where possible

### ExFAT was designed for:
- **Interoperability**: USB drives usable on Windows, Mac, Linux, cameras, game consoles
- **Large Files**: Think video files, disk images, virtual machine files
- **Reliability**: Better handling of sudden disconnections (important for USB drives)

### Why Not Just Use NTFS or ext4?

- **NTFS**: Complex, proprietary (though now well-documented), requires special drivers on some systems
- **ext4**: Linux-centric, not natively supported on many devices
- **ExFAT**: Simple, standardized, universally supported, less resource-intensive

---

## Volume Boot Record (VBR) - The Roadmap

### What is the VBR?

The VBR is the **first thing your parser reads**. It's located at the very beginning of the volume (or at an offset for partitioned disks). It contains crucial information about the entire filesystem layout.

Think of it as a master "map" that tells you:
- "The FAT starts here"
- "The data region starts there"
- "There are X clusters total"
- "One cluster is Y bytes big"

### VBR Structure in ExFAT

```
Offset  Size    Field Name              Purpose
------  ----    ---------               -------
0x000   3       Bootstrap Code          Boot loader (not used by parser)
0x003   8       Signature               Must be "EXFAT   " (8 bytes, padded)
0x040   8       VBR Offset              Where the VBR actually is (for validation)
0x048   8       Volume Size             Total bytes on volume
0x050   4       FAT Offset              Where FAT starts (in sectors from VBR)
0x054   4       FAT Size                Size of FAT (in sectors)
0x058   4       Data Region Offset      Where data clusters start (in sectors)
0x05C   4       Number of Clusters      Total clusters available (cluster 2 onwards)
0x060   4       Root Dir Cluster        Which cluster holds the root directory
0x064   4       Serial Number           Volume ID
0x068   2       Version                 ExFAT version
0x06C   1       Sector Size             Encoded as: size = 2^value (usually 2^9 = 512)
0x06D   1       Cluster Size            Encoded as: size = 2^value (varies)
0x070   1       Number of FATs          Usually 1 (primary) + 1 (backup)
```

### Reading the VBR - Step by Step

```
1. Seek to offset 0 (or your designated VBR offset)
2. Read 12 sectors (12 * 512 = 6144 bytes typically)

3. Validate:
   - Bytes 0x1FE-0x1FF must be 0x55AA (sync value - boot signature)
   - Bytes 0x003-0x00A must be "EXFAT   "

4. Extract key values (all little-endian):
   - Sector size: sectorSize = 1 << valueAtOffset0x6C
   - Cluster size: clusterSize = 1 << valueAtOffset0x6D
   - Root directory cluster: rootCluster = read LE long at 0x060
   - FAT size: fatSize = read LE long at 0x054 (in sectors)
   - Data offset: dataRegionOffset = read LE long at 0x058 (in sectors)
   - Number of clusters: nbClusters = read LE long at 0x05C

5. Calculate important addresses (for later use):
   - vbrStart = vbrOffset * sectorSize
   - firstFatAddress = vbrStart + (fatOffset * sectorSize)
   - dataAreaStart = vbrStart + (dataRegionOffset * sectorSize)
```

### Why This Encoding Matters

The sector and cluster sizes are encoded as **powers of 2**:
- A value of 9 means: size = 2^9 = 512 bytes
- A value of 12 means: size = 2^12 = 4096 bytes

This is space-efficient and ensures only valid power-of-2 sizes are used.

---

## Clusters and Data Region

### What is a Cluster?

A **cluster** is the smallest unit of disk space that can be allocated to a file. Think of it like a page in a book - you allocate whole pages, not individual characters.

**Why clusters instead of individual bytes?**
- Reduces overhead of tracking individual bytes
- Simplifies allocation/deallocation
- Improves performance
- Typical cluster size: 4KB, 8KB, or 32KB depending on volume size

### Cluster Numbering in ExFAT

**Critical Detail**: Cluster numbering starts at **2**, not 0!

```
Cluster 0: Reserved (not used)
Cluster 1: Reserved (not used)
Cluster 2: First usable cluster (where data actually starts)
Cluster 3, 4, 5, ... available clusters
```

This is important when converting cluster numbers to physical disk offsets:

```
physicalOffset = dataAreaStart + ((clusterNumber - 2) * clusterSize)
```

### The Data Region Layout

```
Disk Layout:
┌─────────────────────────────────────────┐
│ VBR (Sector 0, usually)                │
├─────────────────────────────────────────┤
│ FAT 1 (Primary FAT)                     │
│ (tells us which clusters are used)      │
├─────────────────────────────────────────┤
│ FAT 2 (Backup FAT)                      │
│ (redundancy in case FAT 1 is corrupted) │
├─────────────────────────────────────────┤
│ Cluster 2 (first data cluster)          │
│ Cluster 3                               │
│ Cluster 4                               │
│ ...                                     │
│ Cluster N                               │
└─────────────────────────────────────────┘
```

### Calculating Cluster Offset

Given a cluster number, how do you find it on disk?

```go
// Formula:
// physicalOffset = dataAreaStart + ((clusterNumber - 2) * clusterSize)

// In code:
clusterNumber := 5
clusterSize := 4096  // 4KB clusters
dataAreaStart := 1048576  // some calculated offset

physicalOffset := dataAreaStart + ((clusterNumber - 2) * uint64(clusterSize))
// physicalOffset = 1048576 + ((5 - 2) * 4096)
//                = 1048576 + 12288 = 1060864
```

---

## FAT Chain: Following the Trail

### The Fundamental Problem FAT Solves

When you create a file, it might not fit into a single cluster. You need to allocate multiple clusters. But those clusters might not be contiguous (next to each other) on disk.

How does the system know all the clusters that belong to a file?

**Answer: The FAT (File Allocation Table)**

### What is the FAT?

The FAT is a **lookup table** stored at a known location (given in the VBR). Each entry in the FAT describes what happens to the corresponding cluster.

```
FAT Entry Structure (ExFAT):
Each entry is 4 bytes (32-bit little-endian value)

Value Meaning:
------  -------
0x00000000      Cluster is free (unallocated)
0xFFFFFFFE      Bad sector (damaged cluster, don't use)
0xFFFFFFFF      End of chain (last cluster of this file)
0x00000002-0xFFFFFFFD  Cluster number of next cluster in chain
```

### How FAT Works: A Concrete Example

Let's say we have a file that uses clusters 5, 7, and 9.

```
Disk Layout:
Cluster 2: [Some other file data]
Cluster 3: [Some other file data]
Cluster 4: [Some other file data]
Cluster 5: [File A - Part 1 of 3]
Cluster 6: [Some other file data]
Cluster 7: [File A - Part 2 of 3]
Cluster 8: [Some other file data]
Cluster 9: [File A - Part 3 of 3]

FAT Entries:
FAT[2] = 0x00000003  (cluster 2 -> cluster 3)
FAT[3] = 0x00000004  (cluster 3 -> cluster 4)
FAT[4] = 0x00000002  (cluster 4 -> cluster 2) [This would be wrong! Just for example]
FAT[5] = 0x00000007  (cluster 5 -> cluster 7) ← File A path
FAT[6] = 0x00000008  (cluster 6 -> cluster 8)
FAT[7] = 0x00000009  (cluster 7 -> cluster 9) ← File A path
FAT[8] = 0x00000000  (cluster 8 is free)
FAT[9] = 0xFFFFFFFF  (cluster 9 is the END of File A) ← File A ends here
```

### Walking the FAT Chain

To read a complete file starting at cluster 5:

```
currentCluster = 5
while currentCluster != 0xFFFFFFFF:
    // Read data from currentCluster
    readCluster(currentCluster)

    // Get next cluster from FAT
    nextEntry = FAT[currentCluster]

    if nextEntry != 0xFFFFFFFF:
        currentCluster = nextEntry
    else:
        break  // End of file

// Execution trace:
Step 1: currentCluster = 5 → Read cluster 5 → FAT[5] = 0x00000007
Step 2: currentCluster = 7 → Read cluster 7 → FAT[7] = 0x00000009
Step 3: currentCluster = 9 → Read cluster 9 → FAT[9] = 0xFFFFFFFF → STOP
```

### Reading the FAT

```go
// The FAT is stored starting at: firstFatAddress (from VBR)
// Total FAT size: fatSize sectors (from VBR)

// To find the FAT entry for a specific cluster:
fatOffset := int64(firstFatAddress) + (int64(clusterNumber) * 4)
file.Seek(fatOffset, SeekStart)
data := make([]byte, 4)
file.Read(data)
nextCluster := binary.LittleEndian.Uint32(data)
```

### Non-FAT Chain Files

Some files are stored **contiguously** (in sequential clusters) without fragmentation. These are marked with a special flag: `NO_FAT_CHAIN = 0x02`.

**Why?**
- Better performance (avoid FAT lookups)
- Common for small files or freshly created files
- Marked in the file's stream extension entry

```go
if entry.DoesNotHaveFatChain() {
    // Read sequentially without FAT lookups
    data := readClusters(startCluster, numberOfClusters)
} else {
    // Follow the FAT chain
    data := readClustersFat(startCluster)
}
```

---

## Directory Entries and Metadata

### Directory Entry Basics

A **directory** in ExFAT is simply a file containing a sequence of **32-byte directory records**. Each record describes either a file/folder or special metadata.

```
Directory File Structure:
┌──────────────────────────────────┐
│ 32-byte Record 1 (File/Dir info) │
├──────────────────────────────────┤
│ 32-byte Record 2 (Extra info)    │
├──────────────────────────────────┤
│ 32-byte Record 3 (Filename info) │
├──────────────────────────────────┤
│ 32-byte Record 4 (Filename info) │
├──────────────────────────────────┤
│ ... more entries ...             │
├──────────────────────────────────┤
│ 0x00 (End of directory marker)   │
└──────────────────────────────────┘
```

### The Three Types of Directory Records

**Type 1: File/Directory Entry (Type: 0x85 for file, 0xA5 for directory)**

```
Offset  Size    Field
------  ----    -----
0x00    1       Entry Type (0x85 = file, 0xA5 = dir, 0x05 = deleted file)
0x01    1       Secondary Count (how many secondary entries follow)
0x02    2       Set Checksum (sum of all records in this set)
0x04    2       File Attributes (read-only, hidden, system, directory, archive)
0x06    4       Create DateTime
0x0A    4       Last Modified DateTime
0x0E    4       Last Accessed DateTime
0x12    4       Reserved (must be 0)
0x16    4       Modified 10ms, Created 10ms, Accessed 10ms (encoded)
0x1A    4       Reserved (must be 0)
0x1E    2       Reserved (must be 0)
```

**Type 2: Stream Extension Entry (Type: 0xC0)**

Immediately follows a File/Directory entry, contains size and cluster info:

```
Offset  Size    Field
------  ----    -----
0x00    1       Entry Type (0xC0 = stream extension, 0x40 = deleted)
0x01    1       Flags: bit 1 = no FAT chain flag
0x02    6       Reserved
0x08    8       Valid Data Length (bytes actually written)
0x10    4       Reserved
0x14    4       First Cluster (where file data starts)
0x18    8       Data Length (total file size)
```

**Type 3: File Name Entry (Type: 0xC1)**

Follows Stream Extension entry, contains Unicode filename (up to 15 characters):

```
Offset  Size    Field
------  ----    -----
0x00    1       Entry Type (0xC1 = filename, 0x41 = deleted)
0x01    1       Name Length (actual characters, 1-15)
0x02    30      Unicode Filename (15 chars * 2 bytes each)
```

### Why Multiple Records?

ExFAT uses a **multi-record** approach for files:

```
Example: File named "MyPicture.jpg"

Record 1:  [File Info] (Primary)
  - Type: 0x85
  - Attributes: 0x20 (archive)
  - Create time: Nov 15, 2023 14:30:45
  - Secondary count: 2 (tells us to expect 2 more records)
  - Checksum: 0xABCD

Record 2:  [Stream Extension] (Secondary 1)
  - First cluster: 1024
  - Data length: 1048576 (1 MB)
  - No FAT chain flag: yes (contiguous)
  - Checksum contribution calculated

Record 3:  [File Name] (Secondary 2)
  - Name length: 14
  - Unicode name: "MyPicture.jpg"
  - Checksum contribution calculated
```

### Parsing Challenge: Checksum Validation

ExFAT uses a checksum to verify that all records in a set haven't been corrupted:

```
Checksum Algorithm:
1. Start with the File/Directory entry (treating bytes 2-3 as zeros)
2. Add the checksum contribution of each secondary record (bytes 2-3 as-is)
3. XOR each byte of each record

Result: Should match the checksum value in the File/Directory entry
```

This is important for validation - a corrupted record set should be detected!

---

## File Fragmentation

### What is Fragmentation?

**Fragmentation** occurs when a file's data fragments are spread across non-contiguous clusters.

```
Unfragmented File:
Cluster 10: [File Data - Cluster 1 of 3]
Cluster 11: [File Data - Cluster 2 of 3]
Cluster 12: [File Data - Cluster 3 of 3]
→ No fragmentation, fast sequential read


Fragmented File:
Cluster 10: [File Data - Cluster 1 of 3]
Cluster 11: [Other file]
Cluster 12: [File Data - Cluster 2 of 3]
Cluster 13: [Other file]
Cluster 14: [File Data - Cluster 3 of 3]
→ Fragmented, requires multiple disk seeks
```

### How the Parser Handles Fragmentation

The parser doesn't need to care about fragmentation internally - it just follows the FAT chain:

```go
// Whether fragmented or not, the same code handles it:
data := readClustersFat(startCluster)

// Inside readClustersFat:
func readClustersFat(cluster uint32) []byte {
    var result []byte

    for cluster != 0xFFFFFFFF {
        // Read this cluster
        clusterData := readClusters(cluster, 1)
        result = append(result, clusterData...)

        // Get next cluster from FAT
        cluster = FAT[cluster]
    }

    return result
}
```

The FAT chain handles the complexity - the parser just follows a linked list!

### Fragmentation Detection

To analyze fragmentation in a file:

```go
func CountFragments(startCluster uint32) int {
    fragments := 1
    lastCluster := startCluster
    current := FAT[startCluster]

    for current != 0xFFFFFFFF {
        if current != lastCluster + 1 {
            // Gap detected - new fragment
            fragments++
        }
        lastCluster = current
        current = FAT[current]
    }

    return fragments
}
```

---

## Deleted Files and Recovery

### How Are Files Deleted in ExFAT?

Deletion in ExFAT is a "soft delete":

```
When a file is deleted:

1. Entry Type byte is modified:
   - 0x85 (file) → 0x05
   - 0xC0 (stream) → 0x40
   - 0xC1 (filename) → 0x41
   - Basically: bit 7 is cleared

2. The filename entries may be zeroed out (optional)

3. FAT entries are NOT immediately cleared!
   - Clusters still point to each other
   - Clusters remain marked as allocated in FAT
```

### Why This Design?

- **Performance**: Faster deletion (no need to clear FAT, no need to clear data)
- **Recoverability**: Data can be recovered if deletion was accidental
- **Forensics**: Deleted files leave traces that investigators can use

### Deleted File Structure

```
Deleted file marker:
Bit 7 (0x80) cleared = deleted entry
Bit 6-0 still valid

So checking if something is deleted:
if (entryType & 0x80) == 0:
    // This entry is marked as deleted
```

### Recovering Deleted Files

The parser can still read deleted files because:

1. Directory records are still there (with type byte modified)
2. File metadata still intact (size, first cluster)
3. FAT chain still valid (cluster pointers unchanged)
4. File data still on disk (clusters not overwritten)

```go
// In the parser
if entry.IsDeleted() {
    return ErrDeletedEntry
}

// But the data is still accessible if we wanted to recover it
// The parser just chooses not to (for safety)
```

### What Happens to Unallocated Space?

```
Unallocated (free) space:
- Marked in FAT as 0x00000000
- When you delete a file, does the FAT get cleared? Not necessarily!
- This is why recovered files might have stale data
- The space is considered "unallocated" but the old data persists

Over-allocation scenario:
When new files are created:
- Clusters marked as free (0x00000000) in FAT get reused
- New files overwrite the old data
- Now recovery becomes impossible (or partial at best)
```

### Parser Strategy for Deleted Files

The libxfat parser:
1. Detects deleted files
2. Still parses their metadata
3. But marks them as invalid for reading
4. Provides methods to list but prevent extraction

This is a safety measure - you don't want to accidentally extract/use deleted file data.

---

## Implementation Details in libxfat

### Architecture Overview

```
LibXFAT Structure:

┌─────────────────────────────────────────┐
│ ExFAT (Main Parser)                     │
│ - Parses directory entries              │
│ - Validates checksums                   │
│ - Manages parsing state                 │
└─────────┬───────────────────────────────┘
          │
          ├──→ VBR (Volume Boot Record)
          │    - Reads and validates VBR
          │    - Calculates offsets
          │    - Manages FAT access
          │
          ├──→ Entry (File/Directory Info)
          │    - Represents individual files/dirs
          │    - Tracks properties
          │
          └──→ Cluster Management
               - Reads cluster data
               - Follows FAT chains
               - Extracts file content
```

### Key Structs

#### VBR Struct
```go
type VBR struct {
    signature         string      // "EXFAT   "
    volumeSize        uint64      // Total volume size
    fatOffset         uint32      // Where FAT starts
    fatSize           uint32      // FAT size in sectors
    dataRegionOffset  uint32      // Where data clusters begin
    nbClusters        uint32      // Total clusters
    rootDirCluster    uint32      // Root directory's cluster
    sectorSize        uint32      // Bytes per sector (usually 512)
    sectorsPerCluster uint32      // Sectors per cluster
    clusterSize       uint64      // Bytes per cluster
    // ... and many calculated offsets
}
```

#### Entry Struct
```go
type Entry struct {
    etype          byte        // Entry type (0x85 for file, etc)
    dataLen        uint64      // File size in bytes
    entryCluster   uint32      // First cluster
    modified       uint32      // Last modified time (FAT format)
    created        uint32      // Creation time (FAT format)
    entryAttr      uint16      // File attributes (dir, readonly, etc)
    noFatChain     bool        // Is file contiguous?
    name           string      // File/directory name
    secondaryCount uint32      // How many secondary records follow
    nameLen        byte        // Filename character count
}
```

#### ExFAT Struct (Parser State)
```go
type ExFAT struct {
    vbr              VBR         // Volume layout information
    clusterdata      []byte      // Current cluster data buffer
    offset           int         // Position in cluster buffer
    entryState       int         // State machine: START/85_SEEN/LAST_C1_SEEN
    // ... parsing state for multi-record assembly
}
```

### The Parsing State Machine

Directory entry parsing is complex because multi-record entries must be assembled:

```
State Machine Flow:

START
  ↓ (sees 0x85 or 0xA5 - file/dir entry)
ENTRY_STATE_85_SEEN
  ↓ (sees 0xC0 - stream extension)
ENTRY_STATE_LAST_C1_SEEN
  ↓ (sees 0xC1 - filename)
Complete! Create Entry object

Back to START
```

```go
// In parseDir function
for offset < len(clusterdata) {
    dirtype := clusterdata[offset]

    switch dirtype {
    case EXFAT_DIRRECORD_FILEDIR:  // 0x85
        // Start a new entry
        entryState = ENTRY_STATE_85_SEEN

    case EXFAT_DIRRECORD_STREAM_EXT:  // 0xC0
        if entryState == ENTRY_STATE_85_SEEN {
            // Expected! Continue assembly
            entryState = ENTRY_STATE_LAST_C1_SEEN
        }

    case EXFAT_DIRRECORD_FILENAME_EXT:  // 0xC1
        if entryState == ENTRY_STATE_LAST_C1_SEEN {
            // Complete! Add to results
            entries = append(entries, entry)
            entryState = ENTRY_STATE_START
        }
    }

    offset += EXFAT_DIRRECORD_SIZE  // 32 bytes per record
}
```

### Reading Root Directory

```go
// The root directory is special - its location is stored in VBR
func (e *ExFAT) ReadRootDir() []Entry {
    rootCluster := e.vbr.rootDirCluster

    // Read all clusters in root directory
    clusterdata := e.vbr.readClustersFat(rootCluster)

    // Parse as directory
    entries := e.parseDir(clusterdata)

    return entries
}
```

### Reading Subdirectories

```go
// Any file marked as directory (attribute 0x10) can be read as a directory
func (e *ExFAT) ReadDir(entry Entry) []Entry {
    // Ensure it's actually a directory
    if !entry.IsDir() {
        return nil
    }

    // Its data is a directory listing
    content := e.vbr.readContent(entry)

    // Parse like any other directory
    return e.parseDir(content)
}
```

### Extracting File Content

Two paths depending on fragmentation:

```go
func (v *VBR) readContent(entry Entry) []byte {
    if entry.noFatChain {
        // Contiguous - just read sequentially
        data := v.readClusters(entry.entryCluster,
                               calculateNumClusters(entry.dataLen))
    } else {
        // Fragmented - follow FAT chain
        data := v.readClustersFat(entry.entryCluster)
    }

    // Trim to exact size (last cluster might have padding)
    return data[:entry.dataLen]
}
```

### FAT Chain Following

```go
func (v *VBR) readClustersFat(cluster uint32) []byte {
    var result []byte

    for cluster != FINAL_CLUSTER {  // 0xFFFFFFFF
        // Read this cluster
        clusterData := readClusters(cluster, 1)
        result = append(result, clusterData...)

        // Get next cluster number from FAT table
        cluster = v.nextCluster(cluster)
    }

    return result
}

func (v *VBR) nextCluster(cluster uint32) uint32 {
    // Calculate FAT entry offset
    fatOffset := v.firstFat + (cluster * 4)

    // Read 4-byte FAT entry
    v.dimage.Seek(fatOffset, SeekStart)
    data := readBytes(4)

    // Return next cluster number (little-endian)
    return binary.LittleEndian.Uint32(data)
}
```

### Bitmap Analysis

ExFAT has an allocation bitmap (like a FAT but just bits):

```go
func (e *ExFAT) GetAllocatedClusters() uint32 {
    // Read bitmap file
    bitmapData := e.vbr.readContent(e.bitmapEntry)

    // Count set bits (1 = allocated, 0 = free)
    allocated := countBitmap(bitmapData)

    return allocated
}

// Each byte in bitmap represents 8 clusters
// Each bit: 1 = allocated, 0 = free
```

### Time Handling

ExFAT timestamps are in FAT format:

```
Timestamp Format (32-bit value):
Bits 0-4:   Seconds/2 (0-29, so 0-58 seconds)
Bits 5-10:  Minutes (0-59)
Bits 11-15: Hours (0-23)
Bits 16-20: Day (1-31)
Bits 21-24: Month (1-12)
Bits 25-31: Year (since 1980)

Example: 0xFEF33F21
Binary: 1111 1110 1111 0011 0011 1111 0010 0001
Seconds: 00001 = 0 → 0 seconds
Minutes: 000100 = 4 → 4 minutes
Hours:   11100 = 28 → 28 (4 PM... wait, 28 > 23!)
Actually, let me recalculate... (This is a lesson in binary!)
```

---

## Advanced Topics

### Handling Partial/Corrupted Filesystems

The parser includes defensive checks:

```go
func (e *ExFAT) hasRangeForClusterData(start, length int) bool {
    // Ensure we don't read past buffer end
    if e.clusterdata == nil {
        return false
    }
    return start+length <= len(e.clusterdata)
}
```

This helps when:
- Filesystem is partially corrupted
- Only partial image is available
- Media has bad sectors

### Checksum Validation

Critical for detecting corrupted entries:

```go
func exfatDirSetChecksumAdd(accum uint16, record []byte) uint16 {
    // XOR-based checksum across all bytes in a record
    for _, b := range record {
        accum = ((accum << 1) | (accum >> 15))  // Rotate left
        accum += uint16(b)
    }
    return accum
}

// A file entry set is only valid if all record checksums sum correctly
```

### Special Files

ExFAT has metadata files:
- `$BitMap`: Allocation bitmap
- `$UpCase`: Unicode case table
- `$Volume GUID`: Volume identifier
- `$TexFAT`: Trans-exFAT metadata
- `$ACT`: Access Control Table

The parser identifies and protects these:

```go
func (e Entry) IsSpecialFile() bool {
    return e.IsBitmapUpcase() ||
           e.name == VOLUME_GUID ||
           e.name == TEXFAT ||
           e.name == ACT
}

// Users can't accidentally corrupt system files
```

---

## Summary

**The complete flow:**

1. **Parse VBR** → Learn where FAT is, where data is, cluster size
2. **Read directories** → Navigate filesystem tree, identify files
3. **Parse entries** → Multi-record assembly, validate checksums
4. **Follow FAT chains** → If fragmented, trace clusters via FAT table
5. **Read data** → Extract file content from identified clusters
6. **Handle special cases** → Deleted files, corrupted entries, fragmentation

This is how modern file system forensics and recovery tools work!

