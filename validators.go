package libxfat

// Helpers and validators for exFAT directory entries (32-byte records).
// These mirror parts of the robust checks done in the SleuthKit C++ code,
// adapted to the fields/structure available in this Go library.

const maxVolumeLabelChars = 15

// entryTypeNormal masks out the in-use bit (bit 7) from the type byte.
func entryTypeNormal(b byte) byte { return b & 0x7F }

// entryInUse returns true if the type byte indicates the entry is in-use (allocated).
func entryInUse(b byte) bool { return (b & 0x80) != 0 }

// validateVolLabelDentry validates a volume label/no-label entry.
// For label: type masked == 0x03 with in-use bit set, length in [1..15].
// For no-label: same masked type, in-use bit clear, length == 0 and payload zeros.
func (e *ExFAT) validateVolLabelDentry(rec []byte) bool {
	if len(rec) < EXFAT_DIRRECORD_SIZE {
		return false
	}
	if entryTypeNormal(rec[0]) != (EXFAT_DIRRECORD_LABEL & 0x7F) {
		return false
	}
	length := int(rec[1])
	payload := rec[2:EXFAT_DIRRECORD_SIZE]
	if entryInUse(rec[0]) {
		// label present
		if length < 1 || length > maxVolumeLabelChars {
			return false
		}
		return true
	}
	// no-label: length must be 0 and all payload zero
	if length != 0 {
		return false
	}
	for _, b := range payload {
		if b != 0x00 {
			return false
		}
	}
	return true
}

// validateAllocBitmapDentry validates the allocation bitmap entry.
func (e *ExFAT) validateAllocBitmapDentry(rec []byte) bool {
	if len(rec) < EXFAT_DIRRECORD_SIZE {
		return false
	}
	if entryTypeNormal(rec[0]) != (EXFAT_DIRRECORD_BITMAP & 0x7F) {
		return false
	}
	// First cluster at offset 20..23, length at 24..31 (LE)
	firstClust := unpackLELong(rec[20:24])
	lengthBytes := unpackLELongLong(rec[24:32])
	if lengthBytes == 0 {
		return false
	}
	// Required length is ceil(nbClusters/8)
	need := (uint64(e.vbr.nbClusters) + 7) / 8
	if lengthBytes < need {
		return false
	}
	// Cluster range is [2 .. nbClusters+1]
	if firstClust < 2 || uint64(firstClust) > uint64(e.vbr.nbClusters)+1 {
		return false
	}
	return true
}

// validateUpcaseTableDentry validates the upcase table entry.
func (e *ExFAT) validateUpcaseTableDentry(rec []byte) bool {
	if len(rec) < EXFAT_DIRRECORD_SIZE {
		return false
	}
	if entryTypeNormal(rec[0]) != (EXFAT_DIRRECORD_UPCASE & 0x7F) {
		return false
	}
	firstClust := unpackLELong(rec[20:24])
	tableSize := unpackLELongLong(rec[24:32])
	if tableSize == 0 {
		return false
	}
	// Must fit within cluster heap
	heapBytes := uint64(e.vbr.nbClusters) * e.vbr.clusterSize
	if tableSize > heapBytes {
		return false
	}
	if firstClust < 2 || uint64(firstClust) > uint64(e.vbr.nbClusters)+1 {
		return false
	}
	return true
}

// validateFileDentry does a basic check of FILE entry.
func (e *ExFAT) validateFileDentry(rec []byte) bool {
	if len(rec) < EXFAT_DIRRECORD_SIZE {
		return false
	}
	if entryTypeNormal(rec[0]) != (EXFAT_DIRRECORD_FILEDIR & 0x7F) {
		return false
	}
	sc := int(rec[1]) // secondary entries count
	if sc < 1 || sc > 17 {
		return false
	}
	return true
}

// validateFileStreamDentry checks data length and first cluster are sensible.
func (e *ExFAT) validateFileStreamDentry(rec []byte) bool {
	if len(rec) < EXFAT_DIRRECORD_SIZE {
		return false
	}
	if entryTypeNormal(rec[0]) != (EXFAT_DIRRECORD_STREAM_EXT & 0x7F) {
		return false
	}
	dataLen := unpackLELongLong(rec[24:32])
	heapBytes := uint64(e.vbr.nbClusters) * e.vbr.clusterSize
	if dataLen > heapBytes {
		return false
	}
	if dataLen > 0 {
		firstClust := unpackLELong(rec[20:24])
		if firstClust < 2 || uint64(firstClust) > uint64(e.vbr.nbClusters)+1 {
			return false
		}
	}
	return true
}

// validateFileNameDentry ensures type is a filename entry.
func (e *ExFAT) validateFileNameDentry(rec []byte) bool {
	if len(rec) < EXFAT_DIRRECORD_SIZE {
		return false
	}
	return entryTypeNormal(rec[0]) == (EXFAT_DIRRECORD_FILENAME_EXT & 0x7F)
}
