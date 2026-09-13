package libxfat_test

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/aoiflux/libxfat/v2"
)

// Example_walk lists every entry on a volume with its path and its identity.
//
// It has no Output comment because it needs a real image, so it is compiled but
// not run.
func Example_walk() {
	file, err := os.Open("disk.exfat")
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		log.Fatal(err)
	}

	fs, err := libxfat.Open(libxfat.Source{
		Reader: file,
		Size:   info.Size(),
		Strict: true,
	})
	if err != nil {
		log.Fatal(err)
	}

	err = fs.Walk(context.Background(),
		func(path string, parent uint32, entry libxfat.Entry) error {
			// The path is an argument, so entry.Name() is still the bare name.
			id, identifiable := fs.FileID(entry)
			if !identifiable {
				// A synthetic entry, or one carved out of free space.
				fmt.Printf("%s (no identity)\n", path)
				return nil
			}
			fmt.Printf("%s  id=%s parent=%d size=%d\n", path, id, parent, entry.Size())
			return nil
		})
	if err != nil {
		log.Fatal(err)
	}
}

// Example_changedRanges is the shape a change-detection pass takes: intersect a
// list of byte ranges known to have been written against each file's extents, and
// report which files those writes fell inside.
//
// No file content is read. Mapping a file costs only its FAT entries, so a whole
// volume can be mapped for the cost of its FAT.
func Example_changedRanges() {
	var fs *libxfat.ExFAT // opened as above

	// Byte ranges some other layer - a snapshot diff, a journal - reports as
	// written. Offsets are absolute within the same image the volume was opened
	// over, which is what libxfat's own offsets are measured in.
	changed := []struct{ start, end int64 }{
		{0x100000, 0x180000},
	}

	err := fs.Walk(context.Background(),
		func(path string, _ uint32, entry libxfat.Entry) error {
			if entry.IsDir() {
				return nil
			}

			located, err := fs.FragmentOffsetsWithOptions(entry, libxfat.FragmentOptions{})
			if err != nil {
				// An entry with nothing to locate is not a failure of the pass.
				return nil
			}

			for _, r := range located.Ranges {
				for _, c := range changed {
					if r.StartByte >= c.end || c.start >= r.EndByte() {
						continue
					}
					fmt.Printf("%s was written in %s\n", path, r)

					// Say how much the answer is worth. A run the library
					// inferred rather than read is not the same finding as one
					// it walked.
					if located.Truncated || located.ChainBroken || located.Assumed {
						fmt.Printf("  (partial map: truncated=%v broken=%v assumed=%v)\n",
							located.Truncated, located.ChainBroken, located.Assumed)
					}
					return nil
				}
			}
			return nil
		})
	if err != nil {
		log.Fatal(err)
	}
}
