package address

import (
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func TestBech32CodecCache(t *testing.T) {
	acc := NewBech32Codec("cosmos")
	val := NewBech32Codec("cosmosvaloper")
	bz := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}

	sdk.SetAddrCacheEnabled(false)
	accStr, err := acc.BytesToString(bz)
	require.NoError(t, err)
	valStr, err := val.BytesToString(bz)
	require.NoError(t, err)
	_, badPrefixErr := val.StringToBytes(accStr)
	require.Error(t, badPrefixErr)
	sdk.SetAddrCacheEnabled(true)

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
		got[0] = 99 // must not leak into the cache
		got, err = val.StringToBytes(valStr)
		require.NoError(t, err)
		require.Equal(t, bz, got)

		// a cached string with the wrong prefix fails exactly as uncached
		_, err = val.StringToBytes(accStr)
		require.Equal(t, badPrefixErr.Error(), err.Error())
	}

	// mutating the input after encoding does not change the cache
	in := append([]byte(nil), bz...)
	s1, _ := acc.BytesToString(in)
	in[0] = 42
	s2, _ := acc.BytesToString(in)
	require.NotEqual(t, s1, s2)
	back, err := acc.StringToBytes(s2)
	require.NoError(t, err)
	require.Equal(t, in, back)
}

// Over-long strings and byte slices are converted but not cached.
func TestBech32CodecCacheSkipsLongEntries(t *testing.T) {
	acc := NewBech32Codec("cosmos")
	bz := make([]byte, 255) // the longest address VerifyAddressFormat accepts
	for i := range bz {
		bz[i] = byte(i)
	}
	text, err := acc.BytesToString(bz)
	require.NoError(t, err)
	_, ok := encodeCache.get(encodeCacheKey("cosmos", bz))
	require.False(t, ok)

	got, err := acc.StringToBytes(text)
	require.NoError(t, err)
	require.Equal(t, bz, got)
	_, ok = decodeCache.get(text)
	require.False(t, ok)

	// the longest cacheable byte string still encodes to at most 90 chars
	short, err := acc.BytesToString(make([]byte, 20))
	require.NoError(t, err)
	_, ok = encodeCache.get(encodeCacheKey("cosmos", make([]byte, 20)))
	require.True(t, ok)
	require.LessOrEqual(t, len(short), codecCacheMaxLen)
}

func TestEncodeCacheKeyNoCollision(t *testing.T) {
	if encodeCacheKey("a\x00", []byte("b")) == encodeCacheKey("a", []byte("\x00b")) {
		t.Fatal("encode cache keys collide")
	}
}
