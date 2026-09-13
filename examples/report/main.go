// report writes a JSON report of a volume: one document describing the geometry,
// what the format can record, and every entry with its extents and their
// provenance.
//
// It is the shape a consumer that does not link against this library reads a
// volume through - a change-detection pass, a case management system, another
// process entirely - so the interesting part is what survives being written down.
// Every degradation flag is in the document even when false, because a reader
// cannot otherwise tell a verified extent from one this version did not label.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/aoiflux/libxfat"
)

func main() {
	imagePath := flag.String("image", "", "Path to an exFAT image file")
	optimistic := flag.Bool("optimistic", false, "Skip strict VBR offset verification")
	offset := flag.Uint64("offset", 0, "Sector offset where the exFAT volume starts")
	deep := flag.Bool("deep", false, "Also report deleted records and entries carved from free space")
	out := flag.String("out", "", "Write the report here instead of to stdout")
	summary := flag.Bool("summary", false, "Print the aggregate counters instead of the document")
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

	// A report resolves every entry's extents, so on a large image it is worth
	// being able to stop it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	opts := libxfat.ReportOptions{
		IncludeDeleted:            *deep,
		DescendDeletedDirectories: *deep,
		IncludeRecovered:          *deep,
		IncludeSlack:              *deep,
		IncludeUnwritten:          *deep,
		// Fragments is deliberately left at its zero value. Setting
		// AssumeContiguous would fill in extents for deleted entries by assuming
		// their clusters follow one another, and a document is exactly the wrong
		// place for an unrequested guess: the flag saying so is easy to miss and
		// the offsets look like any others.
	}

	if *summary {
		report, err := exfat.ReportWithOptionsContext(ctx, *imagePath, opts)
		if err != nil {
			log.Fatalf("build report: %v", err)
		}
		printSummary(exfat, report)
		return
	}

	writer := os.Stdout
	if *out != "" {
		file, err := os.Create(*out)
		if err != nil {
			log.Fatalf("create output: %v", err)
		}
		defer file.Close()
		writer = file
	}

	if err := exfat.WriteReportWithOptionsContext(ctx, *imagePath, opts, writer); err != nil {
		log.Fatalf("write report: %v", err)
	}
}

func printSummary(exfat *libxfat.ExFAT, report *libxfat.ExFATReport) {
	s := report.Summary()
	meta := report.Filesystem

	fmt.Printf("%s  %s  %d clusters of %d bytes  bytes [%d,%d)\n",
		report.Name, meta.Type, meta.ClusterCount, meta.BlockSize,
		report.StartOffset, report.EndOffset)
	fmt.Printf("label %q  serial %08X  revision %s  active FAT %d of %d\n",
		meta.VolumeLabel, meta.VolumeSerial, meta.Revision, meta.ActiveFAT, meta.FatCount)

	// The recorded hint and the counted answer are both printed because they
	// routinely disagree, and the disagreement is the finding.
	inUse := fmt.Sprintf("%d%%", meta.PercentInUse)
	if !meta.PercentInUseAvailable {
		inUse = "not available"
	}
	fmt.Printf("recorded in use %s  counted %d of %d clusters allocated\n",
		inUse, meta.AllocatedClusters, meta.ClusterCount)
	if meta.VolumeDirty || meta.MediaFailure {
		fmt.Printf("volume flags %#04x: dirty=%v media_failure=%v\n",
			meta.VolumeFlags, meta.VolumeDirty, meta.MediaFailure)
	}
	if meta.BitmapError != "" {
		fmt.Printf("allocation bitmap unreadable: %s\n", meta.BitmapError)
	}

	fmt.Printf("\n%d entries: %d deleted, %d recovered, %d fragmented\n",
		s.Total, s.Deleted, s.Recovered, s.Fragmented)
	for _, kind := range []string{"file", "directory", "metadata", "region", "virtual"} {
		if count := s.TypeCounts[kind]; count > 0 {
			fmt.Printf("  %-10s %d\n", kind, count)
		}
	}
	fmt.Printf("total recorded size %d bytes\n", s.TotalSize)

	// These four are the ones that qualify everything above them.
	fmt.Printf("\nassumed extents %d, truncated %d, self-contradictory records %d, unresolved %d\n",
		s.Assumed, s.Truncated, s.Contradictory, s.Unresolved)
	if !exfat.Capabilities().StableFileIdentity {
		fmt.Println("this format records no reusable file identity: a slot reused after " +
			"a deletion carries its predecessor's FileID exactly")
	}
}
