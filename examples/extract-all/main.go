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
	outDir := flag.String("out", "", "Directory where extracted files will be written")
	optimistic := flag.Bool("optimistic", false, "Skip strict VBR offset verification")
	offset := flag.Uint64("offset", 0, "Sector offset where the exFAT volume starts")
	flag.Parse()

	if *imagePath == "" || *outDir == "" {
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

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("create output directory: %v", err)
	}

	if err := exfat.ExtractAllFiles(rootEntries, *outDir); err != nil {
		log.Fatalf("extract files: %v", err)
	}

	fmt.Printf("Extracted files into %s\n", *outDir)
}
