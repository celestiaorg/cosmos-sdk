package address

import (
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func TestCelestiaBech32CodecCache(t *testing.T) {
	prev := celestiaBech32CodecCacheEnabled
	defer func() { celestiaBech32CodecCacheEnabled = prev }()

	acc := NewBech32Codec("cosmos")
	val := NewBech32Codec("cosmosvaloper")
	bz := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}

	celestiaBech32CodecCacheEnabled = false
	accStr, err := acc.BytesToString(bz)
	require.NoError(t, err)
	valStr, err := val.BytesToString(bz)
	require.NoError(t, err)

	celestiaBech32CodecCacheEnabled = true
	for i := 0; i < 3; i++ {
		s, err := acc.BytesToString(bz)
		require.NoError(t, err)
		require.Equal(t, accStr, s)
		s, err = val.BytesToString(bz)
		require.NoError(t, err)
		require.Equal(t, valStr, s)

		got, err := acc.StringToBytes(accStr)
		require.NoError(t, err)
		require.Equal(t, bz, got)
		got[0] = 99 // must not leak into the memo
		got, err = val.StringToBytes(valStr)
		require.NoError(t, err)
		require.Equal(t, bz, got)

		// wrong prefix still fails on a cached string
		_, err = val.StringToBytes(accStr)
		require.Error(t, err)
		_, err = acc.StringToBytes(valStr)
		require.Error(t, err)
	}
	// mutating the input after encoding does not change the memo
	in := append([]byte(nil), bz...)
	s1, _ := acc.BytesToString(in)
	in[0] = 42
	s2, _ := acc.BytesToString(in)
	require.NotEqual(t, s1, s2)
	back, err := acc.StringToBytes(s2)
	require.NoError(t, err)
	require.Equal(t, in, back)

	// sdk.GetFromBech32 memo (types package)
	restore := sdk.CelestiaSetExecFlags(true)
	defer restore()
	b1, err := sdk.GetFromBech32(accStr, "cosmos")
	require.NoError(t, err)
	require.Equal(t, bz, b1)
	_, err = sdk.GetFromBech32(accStr, "cosmosvaloper")
	require.Error(t, err)
}
