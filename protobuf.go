package douyinim

// Minimal protobuf wire-format primitives (varint + length-delimited) used by
// Douyin's IM HTTP API. Hand-rolled to match the in-page JS exactly — there is
// no .proto schema. 64-bit IDs (snowflake/cursor/timestamp) use uint64.

import "encoding/binary"

// pbWriter builds a protobuf message field by field.
type pbWriter struct {
	buf []byte
}

func (w *pbWriter) varint(v uint64) {
	w.buf = binary.AppendUvarint(w.buf, v)
}

func (w *pbWriter) tag(field, wire int) {
	w.varint(uint64(field)<<3 | uint64(wire))
}

func (w *pbWriter) varintField(field int, v uint64) *pbWriter {
	w.tag(field, 0)
	w.varint(v)
	return w
}

func (w *pbWriter) stringField(field int, s string) *pbWriter {
	w.tag(field, 2)
	w.varint(uint64(len(s)))
	w.buf = append(w.buf, s...)
	return w
}

func (w *pbWriter) bytesField(field int, d []byte) *pbWriter {
	w.tag(field, 2)
	w.varint(uint64(len(d)))
	w.buf = append(w.buf, d...)
	return w
}

func (w *pbWriter) finish() []byte { return w.buf }

// pbReader is a forward-only protobuf reader.
type pbReader struct {
	buf []byte
	pos int
}

func newReader(b []byte) *pbReader { return &pbReader{buf: b} }

func (r *pbReader) eof() bool { return r.pos >= len(r.buf) }

// uvarint reads a varint as uint64 (for IDs / cursors / timestamps).
func (r *pbReader) uvarint() uint64 {
	v, n := binary.Uvarint(r.buf[r.pos:])
	if n <= 0 {
		r.pos = len(r.buf) // malformed: stop
		return 0
	}
	r.pos += n
	return v
}

// skip advances past a field of the given wire type.
func (r *pbReader) skip(wire int) {
	switch wire {
	case 0:
		r.uvarint()
	case 2:
		n := int(r.uvarint())
		r.pos += n
	case 1:
		r.pos += 8
	case 5:
		r.pos += 4
	default:
		r.pos = len(r.buf)
	}
}

// pbField is one decoded field: either a varint value or length-delimited bytes.
type pbField struct {
	num   int
	wire  int
	val   uint64 // wire 0
	bytes []byte // wire 2
}

// readFields decodes all top-level fields of a message into a slice. Repeated
// fields appear multiple times (caller filters by num). Unknown wire types
// (1/5) are skipped but still recorded with num so callers can ignore them.
func readFields(buf []byte) []pbField {
	out := make([]pbField, 0, 16)
	r := newReader(buf)
	for !r.eof() {
		start := r.pos
		tag := r.uvarint()
		num := int(tag >> 3)
		wire := int(tag & 7)
		if num == 0 || num > 6000 {
			break
		}
		switch wire {
		case 0:
			out = append(out, pbField{num: num, wire: 0, val: r.uvarint()})
		case 2:
			n := int(r.uvarint())
			if r.pos+n > len(buf) {
				return out
			}
			out = append(out, pbField{num: num, wire: 2, bytes: buf[r.pos : r.pos+n]})
			r.pos += n
		case 1:
			out = append(out, pbField{num: num, wire: 1})
			r.pos += 8
		case 5:
			out = append(out, pbField{num: num, wire: 5})
			r.pos += 4
		default:
			return out
		}
		if r.pos <= start {
			break
		}
	}
	return out
}

// fieldBytes returns the bytes of the first field with the given number, or nil.
func fieldBytes(fields []pbField, num int) []byte {
	for i := range fields {
		if fields[i].num == num && fields[i].wire == 2 {
			return fields[i].bytes
		}
	}
	return nil
}

// fieldVarint returns the value of the first varint field with the given number.
func fieldVarint(fields []pbField, num int) (uint64, bool) {
	for i := range fields {
		if fields[i].num == num && fields[i].wire == 0 {
			return fields[i].val, true
		}
	}
	return 0, false
}

// fieldString returns the first length-delimited field as a string.
func fieldString(fields []pbField, num int) string {
	return string(fieldBytes(fields, num))
}

// allFields returns every field (any wire) matching num, in order.
func allFields(fields []pbField, num int) []pbField {
	var out []pbField
	for i := range fields {
		if fields[i].num == num {
			out = append(out, fields[i])
		}
	}
	return out
}
