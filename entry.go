package libxfat

// IsContiguousFile is the filter ContiguousFiles and ContiguousFilePaths apply.
//
// It is true for a live, valid, non-directory entry whose stream is contiguous -
// which is to say it is false for every fragmented file on the volume. The name
// says so now; it used to be called IsIndexable, which did not, and a caller could
// reasonably have expected a whole-volume index from a function that silently
// omitted every file large or unlucky enough to have been split.
//
// The behaviour is unchanged, because callers may depend on it. Walk applies no
// such filter and is the right traversal for anything that needs to see every
// file.
func (e Entry) IsContiguousFile() bool {
	return !e.IsDeleted() && !e.IsDir() && !e.IsInvalid() && e.IsContiguous()
}
