package libxfat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDirDetectsDeletedDirectoryEntrySet(t *testing.T) {
	exfat := ExFAT{optimistic: true}

	clusterdata := make([]byte, EXFAT_DIRRECORD_SIZE*4)

	// File directory entry (deleted: 0x05), secondary count = 2 (stream + one name)
	clusterdata[0] = EXFAT_DIRRECORD_DEL_FILEDIR
	clusterdata[1] = 2
	clusterdata[4] = byte(ENTRY_ATTR_DIR_MASK) // mark as directory

	// Stream extension entry (deleted: 0x40)
	stream := EXFAT_DIRRECORD_SIZE
	clusterdata[stream] = EXFAT_DIRRECORD_DEL_STREAM_EXT
	clusterdata[stream+3] = 2 // UTF-16 name length
	clusterdata[stream+20] = 5

	// File name entry (deleted: 0x41) with UTF-16LE name "AB"
	name := EXFAT_DIRRECORD_SIZE * 2
	clusterdata[name] = EXFAT_DIRRECORD_DEL_FILENAME_EXT
	clusterdata[name+2] = 'A'
	clusterdata[name+3] = 0
	clusterdata[name+4] = 'B'
	clusterdata[name+5] = 0

	entries := exfat.parseDir(clusterdata)
	if len(entries) != 1 {
		t.Fatalf("parseDir() entries len = %d, want 1", len(entries))
	}

	entry := entries[0]
	if !entry.IsDeleted() {
		t.Fatal("entry should be detected as deleted")
	}
	if !entry.IsDir() {
		t.Fatal("entry should be detected as directory")
	}
	if got, want := entry.GetName(), "AB"+DELETED; got != want {
		t.Fatalf("entry name = %q, want %q", got, want)
	}
}

func TestIsDeletedDoesNotUseZeroClusterHeuristic(t *testing.T) {
	entry := Entry{
		etype:        EXFAT_DIRRECORD_FILEDIR,
		entryCluster: ZERO_ENTRY_CLUSTER,
	}

	if entry.IsDeleted() {
		t.Fatal("allocated file entry with cluster 0 should not be treated as deleted")
	}
}

func TestParseDeletedDirEntriesScansAcrossZeroRecords(t *testing.T) {
	exfat := ExFAT{optimistic: true}

	clusterdata := make([]byte, EXFAT_DIRRECORD_SIZE*5)

	// First dentry is zero (common in slack/unallocated areas) and should not stop scanning.
	clusterdata[0] = 0x00

	base := EXFAT_DIRRECORD_SIZE
	clusterdata[base] = EXFAT_DIRRECORD_DEL_FILEDIR
	clusterdata[base+1] = 2
	clusterdata[base+4] = byte(ENTRY_ATTR_DIR_MASK)

	stream := EXFAT_DIRRECORD_SIZE * 2
	clusterdata[stream] = EXFAT_DIRRECORD_DEL_STREAM_EXT
	clusterdata[stream+3] = 2

	name := EXFAT_DIRRECORD_SIZE * 3
	clusterdata[name] = EXFAT_DIRRECORD_DEL_FILENAME_EXT
	clusterdata[name+2] = 'Q'
	clusterdata[name+3] = 0
	clusterdata[name+4] = 'R'
	clusterdata[name+5] = 0

	entries := exfat.parseDeletedDirEntries(clusterdata)
	if len(entries) != 1 {
		t.Fatalf("parseDeletedDirEntries() entries len = %d, want 1", len(entries))
	}

	if !entries[0].IsDir() {
		t.Fatal("recovered deleted entry should be detected as directory")
	}
	if !entries[0].IsDeleted() {
		t.Fatal("recovered entry should be deleted")
	}
}

func TestRecoverDeletedEntriesFromUnallocatedClusters(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "deleted-recover.img")
	image, err := os.Create(imagePath)
	if err != nil {
		t.Fatalf("create image: %v", err)
	}
	t.Cleanup(func() {
		_ = image.Close()
	})

	const (
		clusterSize = 512
		nbClusters  = 4
	)

	data := make([]byte, clusterSize*nbClusters)

	// Cluster 2 (offset 0) holds allocation bitmap: only first cluster allocated.
	data[0] = 0x01

	// Cluster 3 (offset 512) has a deleted directory entry set.
	base := clusterSize
	data[base+0] = EXFAT_DIRRECORD_DEL_FILEDIR
	data[base+1] = 2
	data[base+4] = byte(ENTRY_ATTR_DIR_MASK)

	stream := base + EXFAT_DIRRECORD_SIZE
	data[stream+0] = EXFAT_DIRRECORD_DEL_STREAM_EXT
	data[stream+3] = 2

	name := base + (EXFAT_DIRRECORD_SIZE * 2)
	data[name+0] = EXFAT_DIRRECORD_DEL_FILENAME_EXT
	data[name+2] = 'D'
	data[name+3] = 0
	data[name+4] = '1'
	data[name+5] = 0

	if _, err := image.Write(data); err != nil {
		t.Fatalf("write image: %v", err)
	}
	if _, err := image.Seek(0, 0); err != nil {
		t.Fatalf("rewind image: %v", err)
	}

	exfat := ExFAT{
		optimistic: true,
		vbr: VBR{
			dimage:        image,
			clusterSize:   clusterSize,
			nbClusters:    nbClusters,
			dataAreaStart: 0,
			bitmapEntry: Entry{
				etype:        EXFAT_DIRRECORD_BITMAP,
				name:         BITMAP,
				entryCluster: 2,
				dataLen:      1,
				noFatChain:   true,
			},
		},
	}

	deleted, err := exfat.RecoverDeletedEntries()
	if err != nil {
		t.Fatalf("RecoverDeletedEntries() error = %v", err)
	}
	if len(deleted) == 0 {
		t.Fatal("RecoverDeletedEntries() returned no entries")
	}

	found := false
	for _, entry := range deleted {
		if entry.GetName() == "D1"+DELETED {
			found = true
			if !entry.IsDir() {
				t.Fatal("recovered entry D1 should be a directory")
			}
		}
	}

	if !found {
		t.Fatal("expected recovered deleted directory D1 (deleted)")
	}
}
