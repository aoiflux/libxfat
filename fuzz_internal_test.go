package libxfat

import (
	"bytes"
	"context"
	"errors"
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

// walkSeedImage is the smallest volume that gives FuzzWalk something to walk: the
// boot record validTestVBRBytes describes - four 512-byte clusters - with a root
// naming a directory, a deleted directory and a file, and a directory whose only
// record names its own cluster, so each kind of skip is a few mutations away.
func walkSeedImage() []byte {
	const sector = 512
	image := make([]byte, 17*sector)
	copy(image, validTestVBRBytes())

	fat := image[12*sector : 13*sector]
	for cluster := 2; cluster <= 5; cluster++ {
		putLELong(fat[cluster*4:], 0xffffffff)
	}
	clusterAt := func(cluster int) []byte {
		start := (13 + cluster - 2) * sector
		return image[start : start+sector]
	}

	copy(clusterAt(2), buildDir(
		buildEntrySet(testSet{name: "dir", attrs: ENTRY_ATTR_DIR_MASK, cluster: 3, size: sector}),
		buildEntrySet(testSet{name: "gone", attrs: ENTRY_ATTR_DIR_MASK, cluster: 4, size: sector, deleted: true}),
		buildEntrySet(testSet{name: "file", attrs: 0x20, cluster: 5, size: 10}),
	))
	copy(clusterAt(3), buildDir(
		buildEntrySet(testSet{name: "loop", attrs: ENTRY_ATTR_DIR_MASK, cluster: 3, size: sector}),
	))
	return image
}

// TestWalkSeedImageReachesTheWalk keeps FuzzWalk's seed honest: a seed the volume
// opener rejects would leave the fuzzer starting from nothing a walk can read.
func TestWalkSeedImageReachesTheWalk(t *testing.T) {
	data := walkSeedImage()
	fs, err := open(Source{Reader: bytes.NewReader(data), Size: int64(len(data))})
	if err != nil {
		t.Fatalf("open the seed: %v", err)
	}

	var skipped []string
	err = fs.WalkWithOptions(context.Background(), WalkOptions{
		OnSkip: func(path string, _ Entry, reason SkipReason, _ error) error {
			skipped = append(skipped, string(reason)+" "+path)
			return nil
		},
	}, func(string, uint32, Entry) error { return nil })
	if err != nil {
		t.Fatalf("walk the seed: %v", err)
	}
	if len(skipped) != 1 || skipped[0] != string(SkipRevisit)+" /dir/loop" {
		t.Fatalf("skips = %q, want the one revisit of /dir/loop", skipped)
	}
}

// FuzzWalk drives a whole walk, with every option the fuzzer can toggle, and holds
// OnSkip to its contract on whatever tree the bytes describe.
func FuzzWalk(f *testing.F) {
	f.Add(make([]byte, 32), false, false, false, uint8(0))
	seed := walkSeedImage()
	f.Add(seed, false, false, false, uint8(0))
	f.Add(seed, true, true, false, uint8(1))
	f.Add(seed, true, true, true, uint8(0))

	errBudget := errors.New("fuzz budget exhausted")

	f.Fuzz(func(t *testing.T, data []byte, includeDeleted, descendDeleted, includeRecovered bool, maxDepth uint8) {
		fs, err := open(Source{Reader: bytes.NewReader(data), Size: int64(len(data))})
		if err != nil {
			return
		}

		calls := 0
		last := ""
		// pending is set by each walk callback and spent by the skip that follows
		// it, which is how "at most once, and only after the entry was reported"
		// is checked.
		pending := false
		_ = fs.WalkWithOptions(context.Background(), WalkOptions{
			IncludeDeleted:            includeDeleted,
			DescendDeletedDirectories: descendDeleted,
			IncludeRecovered:          includeRecovered,
			MaxDepth:                  int(maxDepth),
			OnSkip: func(path string, e Entry, reason SkipReason, err error) error {
				switch reason {
				case SkipReadError:
					if err == nil {
						t.Fatalf("%s: %s reported without an error", path, reason)
					}
				case SkipDepthLimit, SkipRevisit, SkipInvalidCluster, SkipDeletedUnrecoverable:
					if err != nil {
						t.Fatalf("%s: %s reported with an error %v; no read was attempted", path, reason, err)
					}
				default:
					t.Fatalf("%s: unknown skip reason %q", path, reason)
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("%s: a cancellation was reported as a skip: %v", path, err)
				}
				if !pending || path != last {
					t.Fatalf("OnSkip(%q) does not follow its own walk callback (last reported %q)", path, last)
				}
				pending = false
				if !e.IsDir() {
					t.Fatalf("%s was reported skipped but is not a directory", path)
				}
				return nil
			},
		}, func(path string, _ uint32, _ Entry) error {
			calls++
			if calls > 1<<14 {
				return errBudget
			}
			if path == "" || path[0] != '/' {
				t.Fatalf("path %q is not rooted", path)
			}
			last = path
			pending = true
			return nil
		})
	})
}
