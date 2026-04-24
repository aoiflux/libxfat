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

	allocatedClusters, err := exfat.GetAllocatedClusters()
	if err != nil {
		log.Fatalf("get allocated clusters: %v", err)
	}

	freeClusters, err := exfat.GetFreeClusters()
	if err != nil {
		log.Fatalf("get free clusters: %v", err)
	}

	var metadataEntries int
	for _, entry := range rootEntries {
		if entry.IsSpecialFile() {
			metadataEntries++
		}
	}

	volumeLabel := exfat.GetVolumeLabel()
	if volumeLabel == "" {
		volumeLabel = "(none)"
	}

	fmt.Printf("Volume label: %s\n", volumeLabel)
	fmt.Printf("Cluster size: %d bytes\n", exfat.GetClusterSize())
	fmt.Printf("Used space: %s\n", exfat.GetUsedSpace())
	fmt.Printf("Allocated clusters: %d\n", allocatedClusters)
	fmt.Printf("Free clusters: %d\n", freeClusters)
	fmt.Printf("Root entries: %d\n", len(rootEntries))
	fmt.Printf("Metadata entries in root: %d\n", metadataEntries)
}
