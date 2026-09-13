package libxfat

import (
	"testing"
	"unsafe"
)

// TestEntrySize pins the in-memory size of Entry.
//
// Entry is passed and stored by value everywhere, and a whole-volume walk holds
// one per file: on a large volume the []Entry a walk returns is the single
// largest thing the library allocates, and every byte of padding in this struct
// is multiplied by the file count. The field set has grown repeatedly as
// verification was added, so growth needs to be a deliberate decision rather
// than something noticed later in a profile.
//
// If this fails, that is not automatically a bug. Decide whether the new field
// is worth its cost, check whether it can be packed into existing padding by
// grouping it with fields of its own width, and then update the constant.
func TestEntrySize(t *testing.T) {
	// Pointer-width fields (the two strings) make the expected size
	// architecture-dependent, and the budget below is stated for 64-bit.
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("size budget is stated for 64-bit platforms")
	}

	const want = 104
	if got := unsafe.Sizeof(Entry{}); got != want {
		t.Errorf("unsafe.Sizeof(Entry{}) = %d, want %d\n"+
			"Entry grew or lost its packing; see the comment on this test.", got, want)
	}
}
