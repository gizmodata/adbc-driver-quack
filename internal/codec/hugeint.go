package codec

import "math/big"

// HugeIntParts holds the 64-bit halves of a DuckDB HUGEINT (128-bit
// signed) or UHUGEINT (128-bit unsigned) value. Upper is signed when the
// value is interpreted as HUGEINT; both halves are unsigned when
// interpreted as UHUGEINT.
type HugeIntParts struct {
	Upper int64
	Lower uint64
}

var (
	twoPow64    = new(big.Int).Lsh(big.NewInt(1), 64)
	uint64Mask  = new(big.Int).Sub(twoPow64, big.NewInt(1))
	twoPow64Neg = new(big.Int).Neg(twoPow64)
)

// HugeIntFromSigned splits a signed 128-bit integer into upper/lower
// 64-bit halves matching DuckDB's HUGEINT wire format.
func HugeIntFromSigned(value *big.Int) HugeIntParts {
	// Use mod 2^128 to handle negative values via two's complement.
	mod := new(big.Int).Lsh(big.NewInt(1), 128)
	normalized := new(big.Int).Mod(value, mod)
	if normalized.Sign() < 0 {
		normalized.Add(normalized, mod)
	}

	lowerBig := new(big.Int).And(normalized, uint64Mask)
	upperBig := new(big.Int).Rsh(normalized, 64)

	// Reinterpret upper as signed.
	var upper int64
	if upperBig.BitLen() > 63 {
		upper = int64(upperBig.Uint64() | 0) // wraps via uint64→int64
	} else {
		upper = upperBig.Int64()
	}
	return HugeIntParts{Upper: upper, Lower: lowerBig.Uint64()}
}

// SignedBigInt combines the upper/lower halves into a signed 128-bit
// big.Int (DuckDB HUGEINT).
func (p HugeIntParts) SignedBigInt() *big.Int {
	upper := big.NewInt(p.Upper)
	lower := new(big.Int).SetUint64(p.Lower)
	return new(big.Int).Or(new(big.Int).Lsh(upper, 64), lower)
}

// UnsignedBigInt combines the upper/lower halves treating both as
// unsigned (DuckDB UHUGEINT).
func (p HugeIntParts) UnsignedBigInt() *big.Int {
	upper := new(big.Int).SetUint64(uint64(p.Upper))
	lower := new(big.Int).SetUint64(p.Lower)
	return new(big.Int).Or(new(big.Int).Lsh(upper, 64), lower)
}
