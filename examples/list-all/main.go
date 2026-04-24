package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path"
	"strings"

	"github.com/aoiflux/libxfat"
)

type listingEntry struct {
	typeName string
	marks    string
	size     uint64
	fullPath string
}

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

	entries, err := collectEntries(exfat, rootEntries, "/")
	if err != nil {
		log.Fatalf("walk filesystem: %v", err)
	}
	entries = appendVolumeEntry(entries, exfat.GetVolumeLabel())

	for _, entry := range entries {
		fmt.Printf("%-10s %-28s %10d %s\n", entry.typeName, entry.marks, entry.size, entry.fullPath)
	}
}

func entryType(entry libxfat.Entry) string {
	if isVolumeEntry(entry) {
		return "volume"
	}
	if entry.IsSpecialFile() {
		return "special"
	}
	if entry.IsDir() {
		return "directory"
	}
	return "file"
}

func isVolumeEntry(entry libxfat.Entry) bool {
	return strings.Contains(strings.ToLower(entry.GetName()), "volume")
}

func entryMarks(entry libxfat.Entry) string {
	var marks []string
	if entry.IsDeleted() {
		marks = append(marks, "deleted")
	}
	if entry.IsSpecialFile() {
		marks = append(marks, "special")
	}
	if entry.IsVirtualEntry() {
		marks = append(marks, "virtual")
	}
	if isVolumeEntry(entry) {
		marks = append(marks, "volume")
	}
	if len(marks) == 0 {
		return "-"
	}
	return strings.Join(marks, ",")
}

func appendVolumeEntry(entries []listingEntry, label string) []listingEntry {
	value := label
	if strings.TrimSpace(value) == "" {
		value = "<no-label>"
	}

	volumePath := path.Join("/", "$Volume", value)
	entries = append(entries, listingEntry{
		typeName: "volume",
		marks:    "volume",
		size:     uint64(len(value)),
		fullPath: volumePath,
	})

	return entries
}

func collectEntries(exfat libxfat.ExFAT, entries []libxfat.Entry, basePath string) ([]listingEntry, error) {
	var out []listingEntry

	for _, entry := range entries {
		name := entry.GetName()
		if strings.TrimSpace(name) == "" {
			name = "<no-name>"
		}

		fullPath := path.Join(basePath, name)
		if fullPath == "" {
			fullPath = "/"
		}
		out = append(out, listingEntry{
			typeName: entryType(entry),
			marks:    entryMarks(entry),
			size:     entry.GetSize(),
			fullPath: fullPath,
		})

		if !entry.IsDir() || entry.IsDeleted() || entry.IsVirtualEntry() || entry.IsSpecialFile() {
			continue
		}

		subEntries, err := exfat.ReadDir(entry)
		if err != nil {
			return nil, err
		}

		childEntries, err := collectEntries(exfat, subEntries, fullPath)
		if err != nil {
			return nil, err
		}
		out = append(out, childEntries...)
	}

	return out, nil
}
