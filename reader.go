package libxfat

import (
	"errors"
	"fmt"
	"io"
)

// clampRead reports how many of the n bytes at off lie inside the image.
//
// It is the shared bounds logic behind readAt and readSome, which differ only in
// what they do about a range that is partially outside: readAt refuses it,
// readSome fills what it can. Keeping the checks in one place is what stops the
// two from drifting apart.
//
// A returned error means nothing can be read at all. When the image size is
// unknown (size == 0) bounds checking is disabled and n is allowed in full.
func (v *VBR) clampRead(off int64, n int) (int, error) {
	if v.dimage == nil {
		return 0, ErrNilReader
	}
	if off < 0 {
		return 0, fmt.Errorf("%w: negative offset %d", ErrOutOfBounds, off)
	}

	end := off + int64(n)
	if end < off {
		return 0, fmt.Errorf("%w: offset %d + length %d overflows", ErrOutOfBounds, off, n)
	}

	if v.size > 0 {
		if off >= v.size {
			return 0, io.EOF
		}
		if end > v.size {
			return int(v.size - off), nil
		}
	}
	return n, nil
}

// readAt fills p from the image at absolute byte offset off.
//
// It deliberately reproduces io.ReadFull's error contract so that callers which
// already tolerate a short read at the tail of an image keep working unchanged:
//
//	nil                    the whole of p was filled
//	io.EOF                 nothing could be read (off is at or past the end)
//	io.ErrUnexpectedEOF    p was only partially filled
//
// io.ReaderAt implementations are allowed to return io.EOF together with a
// complete read, which is why the length check comes first.
func (v *VBR) readAt(p []byte, off int64) error {
	if len(p) == 0 {
		return nil
	}

	// When the image size is known, report truncation the same way a short read
	// off the end of an *os.File would, rather than delegating to a reader whose
	// out-of-range behaviour may be less predictable.
	allowed, err := v.clampRead(off, len(p))
	if err != nil {
		return err
	}
	if allowed < len(p) {
		return io.ErrUnexpectedEOF
	}

	n, err := v.dimage.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err == nil || errors.Is(err, io.EOF) {
		if n == 0 {
			return io.EOF
		}
		return io.ErrUnexpectedEOF
	}
	return err
}

// readSome fills as much of p as the image holds and reports how much.
//
// readAt's all-or-nothing contract cannot express a partial tail, which is what
// a reader spanning the end of a truncated image needs, and what buffering a
// window of the FAT needs when the final window runs past the FAT's end. A
// return of (n > 0, nil) is a complete answer: there was nothing more to read.
func (v *VBR) readSome(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	allowed, err := v.clampRead(off, len(p))
	if err != nil {
		return 0, err
	}

	n, err := v.dimage.ReadAt(p[:allowed], off)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, err
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

// isEOF reports whether err is one of the two ways this package says "the image
// ended here": io.EOF from a reader, or ErrEOF from the volume's own bounds
// checks. Callers that tolerate a truncated tail have to accept both, and writing
// the pair out at each of those call sites is how one of them gets forgotten.
func isEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, ErrEOF)
}

// sectionReader returns a reader over length bytes of the image starting at
// off, bounded by the known image size when there is one.
func (v *VBR) sectionReader(off int64, length int64) (*io.SectionReader, error) {
	if v.dimage == nil {
		return nil, ErrNilReader
	}
	if off < 0 || length < 0 {
		return nil, fmt.Errorf("%w: offset %d length %d", ErrOutOfBounds, off, length)
	}
	if end := off + length; end < off {
		return nil, fmt.Errorf("%w: offset %d + length %d overflows", ErrOutOfBounds, off, length)
	} else if v.size > 0 && end > v.size {
		return nil, io.ErrUnexpectedEOF
	}
	return io.NewSectionReader(v.dimage, off, length), nil
}

// safeInt64 converts a uint64 byte count to int64, rejecting values that would
// overflow. Image-derived lengths are attacker-controlled in a forensic
// context, so every conversion goes through here.
func safeInt64(n uint64) (int64, error) {
	const maxInt64 = uint64(1)<<63 - 1
	if n > maxInt64 {
		return 0, fmt.Errorf("%w: length %d exceeds int64", ErrOutOfBounds, n)
	}
	return int64(n), nil
}
