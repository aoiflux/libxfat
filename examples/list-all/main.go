// list-all prints every entry on a volume with its path and its identity.
//
// It is the shortest useful shape of the walk API: one callback, one line per
// entry, no tree of its own. The library composes the paths and supplies each
// entry's parent, so this program holds no state at all.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"

	"github.com/aoiflux/libxfat"
)

func main() {
	imagePath := flag.String("image", "", "Path to an exFAT image file")
	optimistic := flag.Bool("optimistic", false, "Skip strict VBR offset verification")
	offset := flag.Uint64("offset", 0, "Sector offset where the exFAT volume starts")
	deleted := flag.Bool("deleted", false, "Also report deleted records and entries carved from free space")
	flag.Parse()

	if *imagePath == "" {
		flag.Usage()
		os.Exit(2)
	}

	imageFile, err := os.Open(*imagePath)
	if err != nil {
		log.Fatalf("open image: %v", err)
	}
	defer imageFile.Close()

	exfat, err := libxfat.New(imageFile, *optimistic, *offset)
	if err != nil {
		log.Fatalf("parse exFAT: %v", err)
	}

	// The volume label belongs to the volume, not to any entry, so it is reported
	// once here rather than synthesised into the listing as a fake path.
	label, err := exfat.VolumeLabel()
	if err != nil {
		log.Fatalf("read volume label: %v", err)
	}
	if label == "" {
		label = "(none)"
	}
	fmt.Printf("Volume %s  %d clusters of %d bytes  serial %08X\n",
		label, exfat.ClusterCount(), exfat.ClusterSize(), exfat.VolumeSerialNumber())

	// A whole-volume walk on a large image takes a while, so let Ctrl-C stop it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	opts := libxfat.WalkOptions{
		IncludeDeleted:            *deleted,
		DescendDeletedDirectories: *deleted,
		IncludeRecovered:          *deleted,
	}

	err = exfat.WalkWithOptions(ctx, opts,
		func(path string, _ uint32, entry libxfat.Entry) error {
			fmt.Printf("%-10s %-24s %10d  %-14s %s\n",
				entryKind(entry), entryMarks(entry), entry.Size(),
				entryIdentity(exfat, entry), path)
			return nil
		})
	if err != nil {
		log.Fatalf("walk filesystem: %v", err)
	}
}

// entryKind names the same five kinds a report row carries, and in the same order:
// the synthetic entries are special files too, so classifying them first is what
// keeps "region" and "virtual" from being folded into "metadata".
func entryKind(entry libxfat.Entry) string {
	switch {
	case entry.IsRegion():
		return "region"
	case entry.IsVirtualEntry():
		return "virtual"
	case entry.IsSpecialFile():
		return "metadata"
	case entry.IsDir():
		return "directory"
	default:
		return "file"
	}
}

func entryMarks(entry libxfat.Entry) string {
	var marks []string
	if entry.IsDeleted() {
		marks = append(marks, "deleted")
	}
	if entry.IsContiguous() {
		marks = append(marks, "contiguous")
	}
	if entry.Size() != entry.ValidDataSize() {
		marks = append(marks, "partly-unwritten")
	}
	// A record naming a first cluster while its own flags say no allocation is
	// possible contradicts itself. Every formatter sets the bit, so this marks a
	// record worth looking at rather than a routine case.
	if entry.Size() > 0 && !entry.AllocationPossible() {
		marks = append(marks, "no-allocation-flag")
	}
	if len(marks) == 0 {
		return "-"
	}
	return strings.Join(marks, ",")
}

// entryIdentity prints the entry's FileID, or a dash for the entries that have
// none: a synthetic entry has no directory record, and one carved out of free
// space has no surviving parent to be identified against.
func entryIdentity(exfat *libxfat.ExFAT, entry libxfat.Entry) string {
	id, ok := exfat.FileID(entry)
	if !ok {
		return "-"
	}
	return id.String()
}
