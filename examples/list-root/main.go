package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/aoiflux/libxfat"
)

func main() {
	imagePath := flag.String("image", "", "Path to an exFAT image file")
	optimistic := flag.Bool("optimistic", false, "Skip strict VBR offset verification")
	offset := flag.Uint64("offset", 0, "Sector offset where the exFAT volume starts")
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

	rootEntries, err := exfat.ReadRootDir()
	if err != nil {
		log.Fatalf("read root directory: %v", err)
	}

	for _, entry := range rootEntries {
		fmt.Printf("%-16s %-24s size=%-8d cluster=%d\n", entryKind(entry), entry.GetName(), entry.GetSize(), entry.GetEntryCluster())
	}
}

func entryKind(entry libxfat.Entry) string {
	if entry.IsVirtualEntry() {
		return "virtual"
	}
	if entry.IsSpecialFile() {
		return "metadata"
	}
	if entry.IsDir() {
		return "directory"
	}
	return "file"
}
