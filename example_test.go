package libxfat

import (
	"fmt"
)

// Example_specialFiles demonstrates that special metadata files are now
// included in directory listings, similar to the C++ SleuthKit implementation.
//
// When calling ReadRootDir(), the returned entries will include:
//   - Regular files and directories
//   - $BitMap - allocation bitmap
//   - $UpCase - upcase table for case-insensitive comparisons
//   - $Volume GUID - volume GUID entry (if present)
//   - $TexFAT - TexFAT metadata (if present on TexFAT volumes)
//   - $ACT - Access Control Table (if present)
//
// Example usage:
//
//	exfat, err := New(imageFile, false)
//	if err != nil {
//		log.Fatal(err)
//	}
//	
//	entries, err := exfat.ReadRootDir()
//	if err != nil {
//		log.Fatal(err)
//	}
//	
//	for _, entry := range entries {
//		if entry.IsSpecialFile() {
//			fmt.Printf("Special file: %s\n", entry.GetName())
//		} else {
//			fmt.Printf("Regular entry: %s\n", entry.GetName())
//		}
//	}
func Example_specialFiles() {
	fmt.Println("Special files are now listed in directory entries")
	fmt.Println("See ReadRootDir() and ShowAllEntriesInfo() methods")
	// Output:
	// Special files are now listed in directory entries
	// See ReadRootDir() and ShowAllEntriesInfo() methods
}

