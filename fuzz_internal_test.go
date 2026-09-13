package libxfat

import (
	"bytes"
	"testing"
)

// FuzzParseVBRData exercises volume boot record parsing with arbitrary bytes.
// Any input is allowed to be rejected; none is allowed to panic.
func FuzzParseVBRData(f *testing.F) {
	f.Add(make([]byte, VBR_SIZE*int(SECTOR_SIZE)))

	valid := make([]byte, VBR_SIZE*int(SECTOR_SIZE))
	copy(valid[EXFAT_SIGN_OFFSET:], []byte(EXFAT_SIGNATURE))
	valid[SYNC_OFFSET] = 0x55
	valid[SYNC_OFFSET+1] = 0xAA
	f.Add(valid)

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < VBR_SIZE*int(SECTOR_SIZE) {
			data = append(data, make([]byte, VBR_SIZE*int(SECTOR_SIZE)-len(data))...)
		}
		data = data[:VBR_SIZE*int(SECTOR_SIZE)]

		var vbr VBR
		_ = vbr.parseVBRData(data, Source{Strict: true})

		var optimistic VBR
		_ = optimistic.parseVBRData(data, Source{})
	})
}

// FuzzParseDirChunk exercises directory entry set assembly, which is the part
// of the parser that walks attacker-controlled lengths and counts.
func FuzzParseDirChunk(f *testing.F) {
	seed := make([]byte, EXFAT_DIRRECORD_SIZE*3)
	seed[0] = EXFAT_DIRRECORD_FILEDIR
	seed[1] = 2
	seed[EXFAT_DIRRECORD_SIZE] = EXFAT_DIRRECORD_STREAM_EXT
	seed[EXFAT_DIRRECORD_SIZE*2] = EXFAT_DIRRECORD_FILENAME_EXT
	f.Add(seed)

	f.Fuzz(func(t *testing.T, data []byte) {
		newParser := func(optimistic bool) *dirParser {
			vbr := &VBR{nbClusters: 64, clusterSize: 512}
			return newDirParser(vbr, optimistic, false)
		}

		// Verification decides what the library says about an entry set, never
		// which entry sets it finds or what they are called. Holding the two
		// modes against each other on arbitrary bytes is what makes that a
		// property rather than a promise: the mode-dependent blank-on-mismatch
		// path this replaced would fail here on almost any input.
		optimistic := newParser(true).parseDir(data)
		strict := newParser(false).parseDir(data)

		if len(optimistic) != len(strict) {
			t.Fatalf("optimistic parsed %d entries, strict %d", len(optimistic), len(strict))
		}
		for i := range strict {
			if strict[i].name != optimistic[i].name {
				t.Fatalf("entry %d: strict name %q, optimistic %q",
					i, strict[i].name, optimistic[i].name)
			}
		}

		optimisticDeleted := newParser(true).parseDeletedDirEntries(unlocatedChunk, data)
		strictDeleted := newParser(false).parseDeletedDirEntries(unlocatedChunk, data)

		if len(optimisticDeleted) != len(strictDeleted) {
			t.Fatalf("optimistic carved %d deleted entries, strict %d",
				len(optimisticDeleted), len(strictDeleted))
		}
		for i := range strictDeleted {
			if strictDeleted[i].name != optimisticDeleted[i].name {
				t.Fatalf("deleted entry %d: strict name %q, optimistic %q",
					i, strictDeleted[i].name, optimisticDeleted[i].name)
			}
		}
	})
}

// FuzzOpen drives the whole open path over arbitrary image bytes.
func FuzzOpen(f *testing.F) {
	f.Add(make([]byte, 32))

	f.Fuzz(func(t *testing.T, data []byte) {
		fs, err := open(Source{Reader: bytes.NewReader(data), Size: int64(len(data))})
		if err != nil {
			return
		}
		entries, err := fs.ReadRootDir()
		if err != nil {
			return
		}
		for _, entry := range entries {
			_ = entry.ModifiedTime()
			_ = entry.Timestamps()
			_, _, _ = fs.ClusterList(entry)
		}
	})
}
