package libxfat

import (
	"errors"
	"fmt"
	"io"
)

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
	if v.dimage == nil {
		return ErrNilReader
	}
	if off < 0 {
		return fmt.Errorf("%w: negative offset %d", ErrOutOfBounds, off)
	}

	end := off + int64(len(p))
	if end < off {
		return fmt.Errorf("%w: offset %d + length %d overflows", ErrOutOfBounds, off, len(p))
	}

	// When the image size is known, report truncation the same way a short read
	// off the end of an *os.File would, rather than delegating to a reader whose
	// out-of-range behaviour may be less predictable.
	if v.size > 0 {
		if off >= v.size {
			return io.EOF
		}
		if end > v.size {
			return io.ErrUnexpectedEOF
		}
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
