package libxfat

type dirRecordView struct {
	data []byte
}

func newDirRecordView(clusterdata []byte, offset int) (dirRecordView, bool) {
	end := offset + EXFAT_DIRRECORD_SIZE
	if offset < 0 || end > len(clusterdata) {
		return dirRecordView{}, false
	}
	return dirRecordView{data: clusterdata[offset:end]}, true
}

func (r dirRecordView) typeByte() byte {
	return r.data[0]
}

func (r dirRecordView) byteAt(index int) byte {
	return r.data[index]
}

func (r dirRecordView) bytes(start, end int) []byte {
	return r.data[start:end]
}

func (r dirRecordView) le16(start int) uint16 {
	return unpackLEShort(r.data[start : start+2])
}

func (r dirRecordView) le32(start int) uint32 {
	return unpackLELong(r.data[start : start+4])
}

func (r dirRecordView) le64(start int) uint64 {
	return unpackLELongLong(r.data[start : start+8])
}
