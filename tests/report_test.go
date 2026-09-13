package test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aoiflux/libxfat/v2"
)

// reportOf builds a report of the superfloppy fixture with the given options.
func reportOf(t *testing.T, opts libxfat.ReportOptions) *libxfat.ExFATReport {
	t.Helper()

	fs := openSuperfloppy(t, true)
	report, err := fs.ReportWithOptions("superfloppy.exfat", opts)
	if err != nil {
		t.Fatalf("ReportWithOptions(%+v): %v", opts, err)
	}
	return report
}

func rowNamed(t *testing.T, report *libxfat.ExFATReport, path string) libxfat.ExFATFile {
	t.Helper()

	for _, row := range report.Files {
		if row.Path == path {
			return row
		}
	}
	t.Fatalf("report has no row for %q", path)
	return libxfat.ExFATFile{}
}

// TestReportDescribesTheVolume covers the meta block: the geometry a consumer needs
// to make sense of the offsets, and the two facts about this fixture that a report
// would otherwise smooth over - that the volume claims to sit at an LBA it does not
// sit at, and that its recorded fullness hint is not what its bitmap says.
func TestReportDescribesTheVolume(t *testing.T) {
	report := reportOf(t, libxfat.ReportOptions{})
	meta := report.Filesystem

	if report.Name != "superfloppy.exfat" {
		t.Errorf("Name = %q, want the name passed in", report.Name)
	}
	if meta.Type != "exFAT" {
		t.Errorf("Type = %q, want exFAT for a volume with one FAT", meta.Type)
	}
	if meta.BlockSize != sfClusterSize {
		t.Errorf("BlockSize = %d, want %d", meta.BlockSize, sfClusterSize)
	}
	if meta.SectorSize != sfSectorSize {
		t.Errorf("SectorSize = %d, want %d", meta.SectorSize, sfSectorSize)
	}
	if meta.ClusterCount != sfClusterCount {
		t.Errorf("ClusterCount = %d, want %d", meta.ClusterCount, sfClusterCount)
	}
	if meta.RootDirCluster != sfRootCluster {
		t.Errorf("RootDirCluster = %d, want %d", meta.RootDirCluster, sfRootCluster)
	}
	if meta.VolumeSerial != sfSerialNumber {
		t.Errorf("VolumeSerial = %#x, want %#x", meta.VolumeSerial, sfSerialNumber)
	}
	if meta.VolumeLabel != sfVolumeLabel {
		t.Errorf("VolumeLabel = %q, want %q", meta.VolumeLabel, sfVolumeLabel)
	}
	if meta.Revision != "1.0" {
		t.Errorf("Revision = %q, want 1.0", meta.Revision)
	}

	// The bounds are absolute within the reader, which for this image means a base
	// of zero and an end at the volume's recorded size.
	if report.StartOffset != 0 {
		t.Errorf("StartOffset = %d, want 0 for a volume at the start of its image", report.StartOffset)
	}
	if want := int64(sfVolumeSectors) * sfSectorSize; report.EndOffset != want {
		t.Errorf("EndOffset = %d, want %d", report.EndOffset, want)
	}

	// The lie the fixture exists for: the recorded partition offset and the place
	// the volume was found disagree, and both are reported.
	if meta.PartitionOffset != sfClaimedPartitionLBA {
		t.Errorf("PartitionOffset = %d, want the recorded %d",
			meta.PartitionOffset, sfClaimedPartitionLBA)
	}
	if meta.Base != 0 {
		t.Errorf("Base = %d, want 0, which is where the volume actually is", meta.Base)
	}

	// The heap has allocations in it, so a counted zero would mean the bitmap was
	// not read - which is what BitmapError exists to distinguish.
	if meta.BitmapError != "" {
		t.Errorf("BitmapError = %q, want none", meta.BitmapError)
	}
	if meta.AllocatedClusters == 0 {
		t.Error("AllocatedClusters = 0, but the fixture allocates a root and a bitmap")
	}
	if meta.ActiveFAT != 0 || meta.ActiveFatOffset != meta.FatOffset {
		t.Errorf("ActiveFAT = %d at %d, want 0 at the first FAT %d",
			meta.ActiveFAT, meta.ActiveFatOffset, meta.FatOffset)
	}
}

// TestReportRowsCarryIdentity is C7: a report row has to say which file it is, not
// only where it was. Without the identity fields two reports of one volume can be
// diffed by path string alone, which cannot survive a rename.
func TestReportRowsCarryIdentity(t *testing.T) {
	report := reportOf(t, libxfat.ReportOptions{})
	fs := openSuperfloppy(t, true)

	row := rowNamed(t, report, "/docs/notes.txt")
	if !row.Identified {
		t.Fatal("Identified is false for an ordinary file in a real directory")
	}
	if row.ParentFirstCluster != sfDocsCluster {
		t.Errorf("ParentFirstCluster = %d, want /docs at %d", row.ParentFirstCluster, sfDocsCluster)
	}
	if row.FirstCluster != sfNotesCluster {
		t.Errorf("FirstCluster = %d, want %d", row.FirstCluster, sfNotesCluster)
	}
	if row.EntryAbsoluteOffset <= 0 {
		t.Errorf("EntryAbsoluteOffset = %d, want the record's image offset", row.EntryAbsoluteOffset)
	}
	if row.Name != "notes.txt" || row.Type != "file" {
		t.Errorf("Name/Type = %q/%q, want notes.txt/file", row.Name, row.Type)
	}

	// The identity in the row must be the identity the API gives for the same
	// entry; two ways of asking cannot disagree.
	entry := entryNamed(t, fs, "notes.txt")
	id, ok := fs.FileID(entry)
	if !ok {
		t.Fatal("FileID refused an entry the report identified")
	}
	if id.ParentFirstCluster != row.ParentFirstCluster || id.EntrySlotIndex != row.EntrySlotIndex {
		t.Errorf("row identity %d:%d, FileID %s", row.ParentFirstCluster, row.EntrySlotIndex, id)
	}

	// A synthetic entry has no record to be identified against, and says so rather
	// than reporting a parent of zero as though it were one.
	mbr := rowNamed(t, report, "/$MBR")
	if mbr.Identified {
		t.Error("Identified is true for $MBR, which has no directory record")
	}
	if mbr.Type != "region" || !mbr.IsVirtual {
		t.Errorf("$MBR Type = %q, IsVirtual = %v; want region and true", mbr.Type, mbr.IsVirtual)
	}
	if len(mbr.Fragments) != 1 || mbr.Fragments[0].StartCluster != 0 {
		t.Errorf("$MBR fragments = %+v, want one run with no cluster addressing", mbr.Fragments)
	}
}

// TestReportExtentsMatchTheExtentAPI is the consistency gate. The report is a
// second way of asking what FragmentOffsets answers, and if the two can drift then
// one of them is wrong and a consumer cannot tell which.
func TestReportExtentsMatchTheExtentAPI(t *testing.T) {
	report := reportOf(t, libxfat.ReportOptions{IncludeSlack: true, IncludeUnwritten: true})
	fs := openSuperfloppy(t, true)

	row := rowNamed(t, report, "/fragmented.bin")
	if !row.IsFragmented {
		t.Error("IsFragmented is false for the fragmented fixture file")
	}
	if !row.Layout.ChainWalked {
		t.Error("ChainWalked is false, but this file's runs come from the FAT")
	}

	entry := entryNamed(t, fs, "fragmented.bin")
	ranges, err := fs.FragmentOffsets(entry)
	if err != nil {
		t.Fatalf("FragmentOffsets: %v", err)
	}
	if len(row.Fragments) != len(ranges) {
		t.Fatalf("report has %d fragments, FragmentOffsets %d", len(row.Fragments), len(ranges))
	}
	var fileOffset int64
	for i, want := range ranges {
		got := row.Fragments[i]
		if got.StartOffset != want.StartByte || got.Length != want.Length {
			t.Errorf("fragment %d = [%d,+%d), want [%d,+%d)",
				i, got.StartOffset, got.Length, want.StartByte, want.Length)
		}
		if got.EndOffset != want.EndByte() {
			t.Errorf("fragment %d EndOffset = %d, want %d", i, got.EndOffset, want.EndByte())
		}
		if got.StartCluster != want.StartCluster || got.ClusterCount != want.ClusterCount {
			t.Errorf("fragment %d clusters %d+%d, want %d+%d",
				i, got.StartCluster, got.ClusterCount, want.StartCluster, want.ClusterCount)
		}
		// The fragments tile the file, which is what makes them intersectable
		// against a changed-range list without gaps or overlaps.
		if got.FileOffset != fileOffset {
			t.Errorf("fragment %d FileOffset = %d, want %d", i, got.FileOffset, fileOffset)
		}
		fileOffset += got.Length
	}
	if fileOffset != row.Size {
		t.Errorf("fragments cover %d bytes, recorded size is %d", fileOffset, row.Size)
	}

	// Slack and the never-written tail are separate fields because they are
	// separate findings: one is past the recorded size, the other inside it.
	if row.Slack == nil {
		t.Error("IncludeSlack produced no slack for a file that does not end on a cluster")
	} else if row.Slack.FileOffset != row.Size {
		t.Errorf("slack FileOffset = %d, want the file's size %d", row.Slack.FileOffset, row.Size)
	}

	unwritten := rowNamed(t, report, "/unwritten.bin")
	if unwritten.ValidSize >= unwritten.Size {
		t.Fatalf("unwritten.bin ValidSize %d is not short of Size %d",
			unwritten.ValidSize, unwritten.Size)
	}
	if len(unwritten.Unwritten) == 0 {
		t.Error("IncludeUnwritten located nothing for a file with a short ValidDataLength")
	}
	if unwritten.Layout.ValidBytes != unwritten.ValidSize {
		t.Errorf("layout.ValidBytes = %d, row ValidSize = %d; they are the same number",
			unwritten.Layout.ValidBytes, unwritten.ValidSize)
	}
}

// TestReportDefaultOmitsDeletedAndRecovered pins that the report inherits the
// walk's defaults rather than quietly widening them.
func TestReportDefaultOmitsDeletedAndRecovered(t *testing.T) {
	report := reportOf(t, libxfat.ReportOptions{})

	summary := report.Summary()
	if summary.Deleted != 0 || summary.Recovered != 0 {
		t.Errorf("default report has %d deleted and %d recovered rows, want none",
			summary.Deleted, summary.Recovered)
	}
	if summary.Total != len(report.Files) {
		t.Errorf("summary counted %d rows, report has %d", summary.Total, len(report.Files))
	}
	if summary.Assumed != 0 {
		t.Errorf("default report has %d assumed rows; nothing may be hypothesised unasked",
			summary.Assumed)
	}
	if summary.TypeCounts["file"] == 0 || summary.TypeCounts["directory"] == 0 {
		t.Errorf("TypeCounts = %v, want files and directories counted", summary.TypeCounts)
	}
}

// TestReportDeepFindsDeletedAndRecovered is the other half, and checks that the
// deep form still labels its evidence rather than laundering it: a record carved
// out of free space is marked as recovered and not identified.
func TestReportDeepFindsDeletedAndRecovered(t *testing.T) {
	fs := openSuperfloppy(t, true)
	report, err := fs.ReportDeep("superfloppy.exfat")
	if err != nil {
		t.Fatalf("ReportDeep: %v", err)
	}

	summary := report.Summary()
	if summary.Deleted == 0 {
		t.Error("ReportDeep found no deleted rows")
	}
	if summary.Recovered == 0 {
		t.Error("ReportDeep found no carved rows")
	}
	if summary.Assumed != 0 {
		t.Errorf("ReportDeep produced %d assumed rows; deep must not mean weaker evidence",
			summary.Assumed)
	}

	deleted := report.DeletedFiles()
	if len(deleted) != summary.Deleted {
		t.Errorf("DeletedFiles returned %d rows, summary counted %d", len(deleted), summary.Deleted)
	}
	for _, row := range deleted {
		if row.RecordType != 0x05 {
			t.Errorf("%s: RecordType = %#02x, want 0x05 for a deleted record", row.Path, row.RecordType)
		}
	}

	for _, row := range report.RecoveredFiles() {
		if !strings.HasPrefix(row.Path, libxfat.RecoveredPath+"/") {
			t.Errorf("recovered row %q is not under %s", row.Path, libxfat.RecoveredPath)
		}
		if row.Identified {
			t.Errorf("%s: Identified is true, but its parent directory is gone", row.Path)
		}
	}
}

// TestReportRecordsAContradictoryRecord carries the extent API's new finding into
// the document, where a consumer reading the report rather than calling the library
// can still see it.
func TestReportRecordsAContradictoryRecord(t *testing.T) {
	fs := openDamaged(t)
	report, err := fs.Report("damaged.exfat")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}

	row := rowNamed(t, report, "/no-allocation.bin")
	if row.AllocationPossible {
		t.Error("AllocationPossible is true for a record that left the bit clear")
	}
	if !row.Layout.AllocationContradiction {
		t.Error("layout.allocation_contradiction is false for a self-contradictory record")
	}
	if len(row.Fragments) == 0 {
		t.Error("the contradiction cost the row its extents; they are still evidence")
	}
	if got := report.Summary().Contradictory; got != 1 {
		t.Errorf("Summary().Contradictory = %d, want 1", got)
	}

	// The broken and looped chains are degraded in other ways, and each way has to
	// stay distinguishable from the others.
	broken := rowNamed(t, report, "/broken.bin")
	if !broken.Layout.ChainBroken || broken.Layout.LoopDetected {
		t.Errorf("broken.bin layout = %+v, want ChainBroken alone", broken.Layout)
	}
	looped := rowNamed(t, report, "/looped.bin")
	if !looped.Layout.LoopDetected || looped.Layout.ChainBroken {
		t.Errorf("looped.bin layout = %+v, want LoopDetected alone", looped.Layout)
	}
}

// TestReportJSONKeepsProvenanceKeys is the encoding rule: a false provenance flag
// and a zero identity are written out, never omitted.
//
// It matters because the two readings are opposite. A missing chain_walked would
// let a consumer default it to false and treat a FAT-verified extent as
// unverified - or, worse, default it to true and treat a hypothesis as a fact. The
// same goes for an entry_slot_index of 0, which is a real slot.
func TestReportJSONKeepsProvenanceKeys(t *testing.T) {
	fs := openSuperfloppy(t, true)

	var buf bytes.Buffer
	if err := fs.WriteReport("superfloppy.exfat", &buf); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	var decoded struct {
		Name string `json:"name"`
		// Decoded as maps rather than into the report types, because the point is
		// which keys the document carries, and decoding into a struct would fill in
		// every field the encoder left out.
		Filesystem map[string]any   `json:"exfat_meta"`
		Files      []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("the report does not round-trip through JSON: %v", err)
	}
	if len(decoded.Files) == 0 {
		t.Fatal("encoded report has no files")
	}
	if _, ok := decoded.Filesystem["capabilities"]; !ok {
		t.Error("exfat_meta has no capabilities block")
	}

	for _, want := range []string{
		"path", "name", "type", "parent_first_cluster", "entry_slot_index",
		"identified", "first_cluster", "entry_absolute_offset", "record_type",
		"secondary_flags", "allocation_possible", "size", "valid_size", "layout",
		"fragments", "timestamps",
	} {
		if _, ok := decoded.Files[0][want]; !ok {
			t.Errorf("row is missing key %q", want)
		}
	}

	layout, ok := decoded.Files[0]["layout"].(map[string]any)
	if !ok {
		t.Fatalf("layout is not an object: %T", decoded.Files[0]["layout"])
	}
	for _, want := range []string{
		"chain_walked", "no_fat_chain", "assumed", "truncated", "chain_broken",
		"loop_detected", "first_cluster_reallocated", "allocation_contradiction",
		"clusters_walked", "bytes_covered", "valid_bytes",
	} {
		if _, ok := layout[want]; !ok {
			t.Errorf("layout is missing key %q", want)
		}
	}
	// Only present when there is one: an empty error string in every row would
	// read as a field the reader has to check.
	if _, ok := layout["error"]; ok {
		t.Error("layout carries an error key for a row that resolved cleanly")
	}
}

// TestReportCancellationReturnsWhatItHad follows the package's rule that partial
// work is reported rather than discarded - with the exception that the written
// form, which cannot carry a flag saying it is partial, writes nothing at all.
func TestReportCancellationReturnsWhatItHad(t *testing.T) {
	fs := openSuperfloppy(t, true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report, err := fs.ReportWithOptionsContext(ctx, "superfloppy.exfat", libxfat.ReportOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if report == nil {
		t.Fatal("a cancelled report returned nothing at all, not even what it had")
	}

	var buf bytes.Buffer
	werr := fs.WriteReportWithOptionsContext(ctx, "superfloppy.exfat", libxfat.ReportOptions{}, &buf)
	if !errors.Is(werr, context.Canceled) {
		t.Fatalf("WriteReport error = %v, want context.Canceled", werr)
	}
	if buf.Len() != 0 {
		t.Errorf("a cancelled WriteReport wrote %d bytes; a partial document that "+
			"looks complete is worse than none", buf.Len())
	}
}

// TestCapabilitiesStateWhatTheFormatCannotRecord is C6. The false answers are the
// point: a consumer that cannot tell "exFAT has no such field" from "the field was
// empty here" will report changes that did not happen.
func TestCapabilitiesStateWhatTheFormatCannotRecord(t *testing.T) {
	fs := openSuperfloppy(t, true)
	caps := fs.Capabilities()

	for name, got := range map[string]bool{
		"UnicodeNames":          caps.UnicodeNames,
		"CreationTimes":         caps.CreationTimes,
		"ModificationTimes":     caps.ModificationTimes,
		"AccessTimes":           caps.AccessTimes,
		"SubSecondTimestamps":   caps.SubSecondTimestamps,
		"TimezoneOffsets":       caps.TimezoneOffsets,
		"AllocationBitmap":      caps.AllocationBitmap,
		"ValidDataLength":       caps.ValidDataLength,
		"DeclaredContiguity":    caps.DeclaredContiguity,
		"DeletedEntriesSurvive": caps.DeletedEntriesSurvive,
	} {
		if !got {
			t.Errorf("%s is false, but exFAT records it", name)
		}
	}

	for name, got := range map[string]bool{
		"MetadataChangeTimes":  caps.MetadataChangeTimes,
		"POSIXPermissions":     caps.POSIXPermissions,
		"HardLinks":            caps.HardLinks,
		"SymbolicLinks":        caps.SymbolicLinks,
		"ExtendedAttributes":   caps.ExtendedAttributes,
		"SparseFiles":          caps.SparseFiles,
		"Compression":          caps.Compression,
		"CaseSensitive":        caps.CaseSensitive,
		"StableFileIdentity":   caps.StableFileIdentity,
		"IdentityReuseCounter": caps.IdentityReuseCounter,
		"Journal":              caps.Journal,
	} {
		if got {
			t.Errorf("%s is true, but exFAT has no such thing", name)
		}
	}

	// SecondFAT is the one field read from the volume rather than fixed by the
	// format, and this fixture has a single FAT.
	if caps.SecondFAT {
		t.Errorf("SecondFAT is true for a volume recording %d FATs", fs.FatCount())
	}

	// The capabilities a consumer reads out of a stored report must be the same
	// ones the live volume reports.
	report := reportOf(t, libxfat.ReportOptions{})
	if report.Filesystem.Capabilities != caps {
		t.Error("the report's capabilities differ from the volume's")
	}
}

// TestReportFiltersSelectWhatTheySay guards the convenience accessors, which are
// the shape a consumer actually reads a report through.
func TestReportFiltersSelectWhatTheySay(t *testing.T) {
	fs := openSuperfloppy(t, true)
	report, err := fs.ReportDeep("superfloppy.exfat")
	if err != nil {
		t.Fatalf("ReportDeep: %v", err)
	}

	for _, kind := range []string{"file", "directory", "metadata", "region", "virtual"} {
		for _, row := range report.FilesByType(kind) {
			if row.Type != kind {
				t.Errorf("FilesByType(%q) returned a %q row", kind, row.Type)
			}
		}
	}
	for _, row := range report.FragmentedFiles() {
		if len(row.Fragments) < 2 {
			t.Errorf("%s: FragmentedFiles returned a row with %d fragments",
				row.Path, len(row.Fragments))
		}
	}

	// Every row is classified, and the classification covers the whole report.
	kinds := []string{"file", "directory", "metadata", "region", "virtual"}
	for _, row := range report.Files {
		if !slices.Contains(kinds, row.Type) {
			t.Errorf("%s: unclassified type %q", row.Path, row.Type)
		}
	}
}
