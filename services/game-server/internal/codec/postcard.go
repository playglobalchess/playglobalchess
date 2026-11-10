package codec

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Minimal postcard (v1.x) primitives for exactly the EventEnvelope shape — see
// event_seam.go. Postcard rules used here (the only ones this struct needs):
//   - u8             : one raw byte (NOT varint).
//   - u16/u32/u64    : LEB128 varint, little-endian groups.
//   - bool           : one byte, 0 or 1.
//   - string         : uvarint byte-length, then raw UTF-8.
//   - Vec<T>         : uvarint element-count, then each element.
//   - signed i16     : zigzag -> uvarint (postcard encodes signed as zigzag).
//   - f32            : 4 bytes, IEEE-754 little-endian (fixed, not varint).
//   - enum variant   : uvarint discriminant (declaration index), then fields.
// Proven byte-for-byte against the real Rust encoder in event_seam_test.go — do
// NOT change an encoding here without re-pinning the golden vectors.

type pcWriter struct {
	buf []byte
}

func (w *pcWriter) uvarint(v uint64) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	w.buf = append(w.buf, tmp[:n]...)
}

func (w *pcWriter) u8(v uint8) { w.buf = append(w.buf, v) }

func (w *pcWriter) boolean(b bool) {
	if b {
		w.buf = append(w.buf, 1)
	} else {
		w.buf = append(w.buf, 0)
	}
}

func (w *pcWriter) str(s string) {
	w.uvarint(uint64(len(s)))
	w.buf = append(w.buf, s...)
}

func (w *pcWriter) i16(v int16) {
	// zigzag: (n << 1) ^ (n >> 15), then uvarint.
	zz := uint64(uint16((v << 1) ^ (v >> 15)))
	w.uvarint(zz)
}

func (w *pcWriter) f32(v float32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], math.Float32bits(v))
	w.buf = append(w.buf, b[:]...)
}

type pcReader struct {
	buf []byte
	off int
}

func (r *pcReader) uvarint() (uint64, error) {
	v, n := binary.Uvarint(r.buf[r.off:])
	if n <= 0 {
		return 0, fmt.Errorf("codec: truncated/overflow varint at offset %d", r.off)
	}
	r.off += n
	return v, nil
}

// u32 reads a varint and rejects a value past u32 (postcard would too).
func (r *pcReader) u32() (uint32, error) {
	v, err := r.uvarint()
	if err != nil {
		return 0, err
	}
	if v > math.MaxUint32 {
		return 0, fmt.Errorf("codec: u32 varint %d out of range at offset %d", v, r.off)
	}
	return uint32(v), nil
}

func (r *pcReader) u8() (uint8, error) {
	if r.off >= len(r.buf) {
		return 0, fmt.Errorf("codec: truncated u8 at offset %d", r.off)
	}
	v := r.buf[r.off]
	r.off++
	return v, nil
}

func (r *pcReader) boolean() (bool, error) {
	if r.off >= len(r.buf) {
		return false, fmt.Errorf("codec: truncated bool at offset %d", r.off)
	}
	b := r.buf[r.off]
	r.off++
	if b > 1 {
		return false, fmt.Errorf("codec: invalid bool byte %d at offset %d", b, r.off-1)
	}
	return b == 1, nil
}

func (r *pcReader) str() (string, error) {
	n, err := r.uvarint()
	if err != nil {
		return "", err
	}
	if r.off+int(n) > len(r.buf) {
		return "", fmt.Errorf("codec: string len %d exceeds buffer at offset %d", n, r.off)
	}
	s := string(r.buf[r.off : r.off+int(n)])
	r.off += int(n)
	return s, nil
}

func (r *pcReader) i16() (int16, error) {
	zz, err := r.uvarint()
	if err != nil {
		return 0, err
	}
	if zz > 0xffff {
		return 0, fmt.Errorf("codec: i16 zigzag %d out of range at offset %d", zz, r.off)
	}
	u := uint16(zz)
	// inverse zigzag: (u >> 1) ^ -(u & 1)
	return int16((u >> 1) ^ -(u & 1)), nil
}

func (r *pcReader) f32() (float32, error) {
	if r.off+4 > len(r.buf) {
		return 0, fmt.Errorf("codec: truncated f32 at offset %d", r.off)
	}
	bits := binary.LittleEndian.Uint32(r.buf[r.off : r.off+4])
	r.off += 4
	return math.Float32frombits(bits), nil
}
