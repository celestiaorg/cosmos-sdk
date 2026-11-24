package types

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/types/bech32"
)

func bech32Cached() (n int) {
	for i := range bech32Cache {
		bech32Cache[i].mu.RLock()
		n += len(bech32Cache[i].m)
		bech32Cache[i].mu.RUnlock()
	}
	return n
}

func TestDecodeBech32CachedMatchesUncached(t *testing.T) {
	addr, err := bech32.ConvertAndEncode("test", []byte("0123456789abcdef0123"))
	require.NoError(t, err)
	for _, in := range []string{addr, addr, "test1invalid", addr[:len(addr)-1] + "x"} {
		wantHrp, wantBz, wantErr := bech32.DecodeAndConvert(in)
		hrp, bz, err := decodeBech32Cached(in)
		require.Equal(t, wantErr, err, in)
		require.Equal(t, wantHrp, hrp, in)
		require.Equal(t, wantBz, bz, in)
	}
	_, err = GetFromBech32(addr, "other")
	require.EqualError(t, err, "invalid Bech32 prefix; expected other, got test")

	_, first, err := decodeBech32Cached(addr)
	require.NoError(t, err)
	first[0] ^= 0xff
	_, second, err := decodeBech32Cached(addr)
	require.NoError(t, err)
	require.Equal(t, []byte("0123456789abcdef0123"), second)
}

func TestDecodeBech32CachedSkipsInvalidLongAndDisabled(t *testing.T) {
	before := bech32Cached()
	_, _, err := decodeBech32Cached("test1invalid")
	require.Error(t, err)
	require.Equal(t, before, bech32Cached())

	long, err := bech32.ConvertAndEncode("test", make([]byte, 64))
	require.NoError(t, err)
	_, _, err = decodeBech32Cached(long)
	require.NoError(t, err)
	require.Equal(t, before, bech32Cached())

	SetAddrCacheEnabled(false)
	defer SetAddrCacheEnabled(true)
	addr, err := bech32.ConvertAndEncode("test", []byte("disabled-cache-addr!"))
	require.NoError(t, err)
	_, _, err = decodeBech32Cached(addr)
	require.NoError(t, err)
	require.Equal(t, before, bech32Cached())
}

func TestDecodeBech32CachedConcurrent(t *testing.T) {
	addr, err := bech32.ConvertAndEncode("test", []byte("concurrent-cache-adr"))
	require.NoError(t, err)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, bz, err := decodeBech32Cached(addr)
			require.NoError(t, err)
			require.Equal(t, []byte("concurrent-cache-adr"), bz)
			bz[0] = 0
		}()
	}
	wg.Wait()
}
