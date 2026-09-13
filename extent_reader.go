package libxfat

import (
	"fmt"
	"io"
	"sort"
)

// extentReader presents a list of image byte ranges as one contiguous stream.
//
// It is how content is read without writing anything to the host filesystem: the
// ranges say where a file's bytes are, and this turns a read at a file offset
// into a read at the right image offset, crossing runs as needed.
//
// It holds no reference to the entry it came from and never re-walks the FAT: the
// ranges are resolved once by the caller and are immutable afterwards, which is
// what lets the cursor-free ReaderAt form be used from several goroutines.
type extentReader struct {
	v      *VBR
	ranges []Range
	// starts[i] is the offset within the stream at which ranges[i] begins, so a
	// read can find its run by binary search rather than by walking the list.
	starts []int64
	size   int64
	pos    int64
}

func newExtentReader(v *VBR, ranges []Range) *extentReader {
	starts := make([]int64, len(ranges))
	var total int64
	for i, r := range ranges {
		starts[i] = total
		total += r.Length
	}
	return &extentReader{v: v, ranges: ranges, starts: starts, size: total}
}

// Size is the number of bytes the ranges actually cover, which is not necessarily
// what the directory entry claimed. Reads are bounded by this, never by the claim.
func (r *extentReader) Size() int64 { return r.size }

// clone returns an independent reader over the same ranges, with its own cursor.
// The ranges are shared because they are read-only.
func (r *extentReader) clone() *extentReader {
	return &extentReader{v: r.v, ranges: r.ranges, starts: r.starts, size: r.size}
}

func (r *extentReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("%w: negative offset %d", ErrOutOfBounds, off)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= r.size {
		return 0, io.EOF
	}

	read := 0
	for read < len(p) {
		pos := off + int64(read)
		if pos >= r.size {
			return read, io.EOF
		}

		i := sort.Search(len(r.starts), func(i int) bool {
			return r.starts[i] > pos
		}) - 1
		if i < 0 {
			return read, io.EOF
		}

		run := r.ranges[i]
		delta := pos - r.starts[i]
		want := run.Length - delta
		if remaining := int64(len(p) - read); want > remaining {
			want = remaining
		}

		n, err := r.v.readSome(p[read:read+int(want)], run.StartByte+delta)
		read += n
		if err != nil {
			if err == io.EOF {
				return read, io.EOF
			}
			return read, err
		}
		if n == 0 {
			return read, io.EOF
		}
	}
	return read, nil
}

func (r *extentReader) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}
	n, err := r.ReadAt(p, r.pos)
	r.pos += int64(n)
	if err == io.EOF && n > 0 {
		return n, nil
	}
	return n, err
}

func (r *extentReader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.pos + offset
	case io.SeekEnd:
		abs = r.size + offset
	default:
		return 0, fmt.Errorf("%w: invalid whence %d", ErrOutOfBounds, whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("%w: seek to %d", ErrOutOfBounds, abs)
	}
	r.pos = abs
	return abs, nil
}

// WriteTo streams the whole extent to w, one run at a time.
//
// It exists so extraction copies run by run rather than through a fixed-size
// buffer chopped across run boundaries, and so io.Copy picks it up automatically.
func (r *extentReader) WriteTo(w io.Writer) (int64, error) {
	var written int64
	for i, run := range r.ranges {
		start := run.StartByte
		length := run.Length
		if r.pos > r.starts[i] {
			skip := r.pos - r.starts[i]
			if skip >= length {
				continue
			}
			start += skip
			length -= skip
		}

		section, err := r.v.sectionReader(start, length)
		if err != nil {
			return written, err
		}
		n, err := io.CopyN(w, section, length)
		written += n
		r.pos += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}
