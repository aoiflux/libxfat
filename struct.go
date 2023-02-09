package libxfat

import (
	"os"
	"strings"
)

type VBR struct {
	signature         string
	vbrOffset         uint64
	volumeSize        uint64
	fatOffset         uint32
	fatSize           uint32
	dataRegionOffset  uint32
	nbClusters        uint32
	rootDirCluster    uint32
	sn                []byte
	version           uint16
	sectorSize        uint32
	sectorsPerCluster uint32
	clusterSize       uint64
	vbrStart          uint64
	firstFat          uint64
	percentInUse      byte
	dataAreaStart     uint64
	dimage            *os.File
	volumeLabel       string
	bitmcapCluster    uint32
	bitmapLength      uint64
	upcaseCluster     uint32
	upcaseLength      uint64
	bitmapEntry       Entry
}

type Entry struct {
	etype          byte
	dataLen        uint64
	entryCluster   uint32
	modified       uint32
	created        uint32
	accessed       uint32
	modified10ms   byte
	created10ms    byte
	entryAttr      uint16
	noFatChain     bool
	name           string
	seenRecords    []byte
	secondaryCount uint32
	nameLen        byte
	readNameLen    uint32
	validDataLen   uint64
}

func (e Entry) IsInvalid() bool {
	return !e.IsValid() || e.IsBitmapUpcase()
}
func (e Entry) IsValid() bool {
	return e.etype == EXFAT_DIRRECORD_FILEDIR
}
func (e Entry) IsBitmapUpcase() bool {
	return e.name == BITMAP || e.name == UPCASE || e.etype == EXFAT_DIRRECORD_BITMAP || e.etype == EXFAT_DIRRECORD_UPCASE
}
func (e Entry) GetNameLength() byte {
	return e.nameLen
}
func (e Entry) GetName() string {
	return e.name
}
func (e Entry) IsFile() bool {
	return !e.IsDir()
}
func (e Entry) IsDir() bool {
	return e.entryAttr&ENTRY_ATTR_DIR_MASK > 0
}
func (e Entry) GetEntryCluster() uint32 {
	return e.entryCluster
}
func (e Entry) GetSize() uint64 {
	return e.dataLen
}
func (e Entry) DoesNotHaveFatChain() bool {
	return e.noFatChain
}
func (e Entry) HasFatChain() bool {
	return !e.noFatChain
}
func (e Entry) IsDeleted() bool {
	return e.entryCluster == ZERO_ENTRY_CLUSTER || e.etype == EXFAT_DIRRECORD_DEL_FILEDIR
}
func (e Entry) HasNoName() bool {
	ename := strings.TrimSpace(e.name)
	ename = strings.TrimSuffix(ename, DELETED)
	ename = strings.ReplaceAll(ename, " ", "")
	return ename == ""
}
func (e Entry) NonParsable() bool {
	return e.IsDeleted() || e.IsFile() || e.IsInvalid() || e.HasNoName()
}

type ExFAT struct {
	vbr          VBR
	virtualEntry Entry
	entry        Entry
	offset       int
	remainingSC  int
	entryState   int
	clusterdata  []byte
	dirtype      byte
	optimistic   bool
}

func New(imagefile *os.File, optimistic bool, offset ...uint64) (ExFAT, error) {
	if len(offset) < 1 {
		offset = append(offset, 0)
	}
	var exfatdata ExFAT
	var err error
	exfatdata.optimistic = optimistic
	exfatdata.vbr, err = parseVBR(imagefile, offset[0], exfatdata.optimistic)
	return exfatdata, err
}
func (e *ExFAT) initEntryState(clusetrdata []byte, offset, remainingSC, entryState int) {
	e.virtualEntry = Entry{}
	e.entry = Entry{}
	e.clusterdata = clusetrdata
	e.offset = offset
	e.remainingSC = remainingSC
	e.entryState = entryState
}
