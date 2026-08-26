package quack

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubstituteParams(t *testing.T) {
	sql, err := substituteParams(`SELECT * FROM "t?" WHERE a > ? AND b = ? -- ? no
 AND c = '?' /* ? */ AND d = ?`, []any{int64(3), "x'y", nil})
	require.NoError(t, err)
	require.Equal(t, `SELECT * FROM "t?" WHERE a > 3 AND b = 'x''y' -- ? no
 AND c = '?' /* ? */ AND d = NULL`, sql)
	_, err = substituteParams("SELECT ?", nil)
	require.Error(t, err)
	_, err = substituteParams("SELECT 1", []any{int64(1)})
	require.Error(t, err)
	require.Equal(t, "'\\x01\\xFF'::BLOB", renderLiteral([]byte{1, 0xFF}))
	require.Equal(t, "-0.05", string(decimalLiteral(bigInt(-5), 2)))
	require.Equal(t, "TRUE", renderLiteral(true))
}

func bigInt(n int64) *big.Int { return big.NewInt(n) }
