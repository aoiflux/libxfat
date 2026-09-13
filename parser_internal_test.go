package libxfat

import "testing"

// TestSubdirectoryCannotRepointVolumeStructures is the regression test for the
// bug the parser extraction fixed.
//
// $BitMap and $UpCase are root-directory records by specification. The parser
// used to write them onto the shared volume from whichever directory it happened
// to be reading, so a record in any subdirectory silently repointed the
// allocation bitmap for the whole volume - and with it every answer
// getUnallocatedClusters and RecoverDeletedEntries give.
//
// The parse still reports such a record as an entry, because it is there and
// hiding it would be its own kind of lie. What it must not do is believe it.
func TestSubdirectoryCannotRepointVolumeStructures(t *testing.T) {
	vbr := &VBR{nbClusters: 64, clusterSize: 512}

	// A well-formed $BitMap record: type 0x81, first cluster 9, a length that
	// matches the cluster count so the validator accepts it.
	record := make([]byte, EXFAT_DIRRECORD_SIZE*2)
	record[0] = EXFAT_DIRRECORD_BITMAP
	putLE32(record[20:24], 9)
	putLE64(record[24:32], (64+7)/8)

	parser := newDirParser(vbr, true, false)
	entries := parser.parseDir(record)

	if len(entries) == 0 {
		t.Fatal("parseDir dropped the $BitMap record entirely; it should be reported")
	}
	found := false
	for _, e := range entries {
		if e.name == BITMAP {
			found = true
		}
	}
	if !found {
		t.Fatal("the $BitMap record was not reported as an entry")
	}

	// It reached parseOutputs, which is where a root parse would pick it up...
	if !parser.out.sawBitmap {
		t.Fatal("the record did not reach parseOutputs")
	}
	// ...but nothing was written onto the volume, because only the root parse
	// publishes, and this parse's outputs are discarded by readDirEntries.
	if vbr.bitmapEntry.entryCluster != 0 || vbr.bitmapEntry.name != "" {
		t.Fatalf("parsing a subdirectory record mutated the volume's $BitMap: %+v",
			vbr.bitmapEntry)
	}
	if vbr.upcaseTable != nil {
		t.Fatal("parsing a subdirectory record invalidated the volume's up-case table")
	}
}

// TestPublishOnlyFromRootParse pins the two halves of the rule together: the
// outputs a parse collects are installed only when the caller chooses to.
func TestPublishOnlyFromRootParse(t *testing.T) {
	fs := &ExFAT{vbr: VBR{nbClusters: 64, clusterSize: 512}}

	out := parseOutputs{
		volumeLabel: "EVIDENCE",
		sawLabel:    true,
		bitmapEntry: Entry{name: BITMAP, entryCluster: 9, dataLen: 8},
		sawBitmap:   true,
	}

	if fs.publishedVolumeLabel() != "" {
		t.Fatal("volume label set before any parse published one")
	}

	fs.publish(out)

	if fs.publishedVolumeLabel() != "EVIDENCE" {
		t.Fatalf("volume label = %q after publish, want %q", fs.publishedVolumeLabel(), "EVIDENCE")
	}
	if fs.vbr.bitmapEntry.entryCluster != 9 {
		t.Fatalf("published $BitMap cluster = %d, want 9", fs.vbr.bitmapEntry.entryCluster)
	}
}

// TestPublishDropsStaleUpcaseTable checks the invalidation the old inline write
// did, which publish has to preserve: a decompressed table describes one $UpCase
// stream and must not survive being pointed at another.
func TestPublishDropsStaleUpcaseTable(t *testing.T) {
	fs := &ExFAT{vbr: VBR{nbClusters: 64, clusterSize: 512}}
	fs.vbr.upcaseEntry = Entry{name: UPCASE, entryCluster: 5, dataLen: 128}
	fs.vbr.upcaseTable = upcaseTableFixture()

	// Republishing the same stream keeps the table: nothing changed.
	fs.publish(parseOutputs{upcaseEntry: fs.vbr.upcaseEntry, sawUpcase: true})
	if fs.vbr.upcaseTable == nil {
		t.Fatal("publishing an identical $UpCase entry dropped a still-valid table")
	}

	// A different stream must drop it.
	fs.publish(parseOutputs{
		upcaseEntry: Entry{name: UPCASE, entryCluster: 7, dataLen: 128},
		sawUpcase:   true,
	})
	if fs.vbr.upcaseTable != nil {
		t.Fatal("publishing a different $UpCase entry kept the stale table")
	}
}

func upcaseTableFixture() []uint16 { return []uint16{'a', 'b', 'c'} }

func putLE32(dst []byte, v uint32) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v >> 16)
	dst[3] = byte(v >> 24)
}

func putLE64(dst []byte, v uint64) {
	for i := 0; i < 8; i++ {
		dst[i] = byte(v >> (8 * i))
	}
}
