package libxfat

import "fmt"

func (e Entry) GetDataLen() string {
	return humanize(e.dataLen)
}

func (e Entry) GetValidDataLen() string {
	return humanize(e.validDataLen)
}

func getDirEntry(entry Entry, path string, long bool) string {
	if long {
		return getDirEntryLong(entry, path)
	}

	fullpath := path + entry.name
	modifiedTime := getDateTimeString(entry.modified, uint32(entry.modified10ms))
	fileAttributes := getFileAttributes(entry.entryAttr)

	shortname := fmt.Sprintf("%s %s %d %d %s", modifiedTime, fileAttributes, entry.entryCluster, entry.dataLen, fullpath)
	return shortname
}

func getDirEntryLong(entry Entry, path string) string {
	fullpath := path + entry.name
	nfc := "fat"

	if entry.noFatChain {
		nfc = "nfc"
	}

	var deleted string
	typestr := fmt.Sprintf("0x%v", entry.etype)
	if entry.IsDeleted() {
		deleted = DELETED
	}
	fileAttributes := getFileAttributes(entry.entryAttr)
	modifiedTime := getDateTimeString(entry.modified, uint32(entry.modified10ms))
	accessedtime := getDateTimeString(entry.accessed, 0)
	createdTime := getDateTimeString(entry.created, uint32(entry.created10ms))

	longname := fmt.Sprintf("%s i=%d l=%d %s m=%s a=%s b=%s sc=%d %s %s%s", typestr, entry.entryCluster, entry.dataLen, fileAttributes, modifiedTime, accessedtime, createdTime, entry.secondaryCount, nfc, fullpath, deleted)
	return longname
}
