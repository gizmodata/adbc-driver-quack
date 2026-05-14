// Package message decodes (and encodes) DuckDB Quack DataChunks and the
// surrounding protocol messages. It is a clean-room Go port of the
// message/ package in the sibling JDBC driver.
package message

// Validity bitmap helpers — one bit per row, packed into a []uint64
// matching the wire format. Bit value 1 means the row is valid; 0 means
// it is null. A nil bitmap means "every row is valid" (cheaper for the
// common non-nullable case).

// ValidityWordCount returns the number of uint64 words required to hold
// the validity bitmap for count rows.
func ValidityWordCount(count int) int {
	return (count + 63) >> 6
}

// ValidityWireByteCount returns the byte count the wire format uses for
// the bitmap (64-bit aligned).
func ValidityWireByteCount(count int) int {
	return ValidityWordCount(count) * 8
}

// ValidityIsValid reports whether row is valid. A nil bitmap counts every row as valid.
func ValidityIsValid(validity []uint64, row int) bool {
	if validity == nil {
		return true
	}
	return validity[row>>6]&(1<<uint(row&63)) != 0
}

// ValidityIsNull is the inverse of ValidityIsValid.
func ValidityIsNull(validity []uint64, row int) bool {
	return !ValidityIsValid(validity, row)
}

// ValidityFromBytes decodes the wire-format validity bytes into a bitmap.
func ValidityFromBytes(bytes []byte, count int) []uint64 {
	out := make([]uint64, ValidityWordCount(count))
	for i := range out {
		var v uint64
		base := i * 8
		for b := 0; b < 8 && base+b < len(bytes); b++ {
			v |= uint64(bytes[base+b]) << (uint(b) * 8)
		}
		out[i] = v
	}
	return out
}

// ValidityToBytes encodes a bitmap into the wire-format byte representation.
// A nil validity (= all valid) encodes as an all-ones bitmap up to count rows.
func ValidityToBytes(validity []uint64, count int) []byte {
	out := make([]byte, ValidityWireByteCount(count))
	if validity == nil {
		for i := 0; i < count; i++ {
			out[i>>3] |= byte(1 << uint(i&7))
		}
		return out
	}
	for i, v := range validity {
		for b := 0; b < 8; b++ {
			out[i*8+b] = byte(v >> (uint(b) * 8))
		}
	}
	return out
}

// ValidityAllValid allocates a bitmap with every row marked valid up to count.
func ValidityAllValid(count int) []uint64 {
	out := make([]uint64, ValidityWordCount(count))
	full := count >> 6
	for i := 0; i < full; i++ {
		out[i] = ^uint64(0)
	}
	rem := count & 63
	if rem > 0 && full < len(out) {
		out[full] = (uint64(1) << uint(rem)) - 1
	}
	return out
}

// ValiditySetValid sets the bit for row to 1.
func ValiditySetValid(validity []uint64, row int) {
	validity[row>>6] |= 1 << uint(row&63)
}

// ValiditySetNull sets the bit for row to 0.
func ValiditySetNull(validity []uint64, row int) {
	validity[row>>6] &^= 1 << uint(row&63)
}
