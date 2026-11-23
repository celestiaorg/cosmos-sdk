package types

import (
	"bytes"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/types/bech32"
)

func bech32CacheLen() int {
	n := 0
	bech32DecodeCache.Range(func(_, _ any) bool { n++; return true })
	return n
}

func resetBech32Cache() {
	bech32DecodeCache.Range(func(k, _ any) bool { bech32DecodeCache.Delete(k); return true })
	bech32DecodeCacheSize = 0
}

// TestBech32DecodeCacheMatchesDecode checks that GetFromBech32 returns the same
// bytes and errors with the memo on and off, and that callers cannot mutate the
// memo through a returned slice.
func TestBech32DecodeCacheMatchesDecode(t *testing.T) {
	prev := ExecutionCachesEnabled()
	t.Cleanup(func() { SetExecutionCaches(prev); resetBech32Cache() })
	resetBech32Cache()

	addr20, err := bech32.ConvertAndEncode("cosmos", bytes.Repeat([]byte{7}, 20))
	require.NoError(t, err)
	addr32, err := bech32.ConvertAndEncode("cosmosvaloper", bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	inputs := []struct{ s, prefix string }{
		{addr20, "cosmos"}, {addr20, "cosmosvaloper"}, {addr32, "cosmosvaloper"},
		{"", "cosmos"}, {"cosmos1invalid", "cosmos"}, {addr20[:len(addr20)-1] + "q", "cosmos"},
	}
	type result struct {
		bz  []byte
		err string
	}
	run := func(on bool) []result {
		SetExecutionCaches(on)
		var out []result
		for pass := 0; pass < 2; pass++ { // second pass hits the memo
			for _, in := range inputs {
				bz, err := GetFromBech32(in.s, in.prefix)
				r := result{bz: bz}
				if err != nil {
					r.err = err.Error()
				}
				out = append(out, r)
				if len(bz) > 0 {
					bz[0] ^= 0xff // must not leak into the memo
				}
			}
		}
		return out
	}
	off := run(false)
	on := run(true)
	require.Equal(t, off, on)
	require.Equal(t, 2, bech32CacheLen())
}

// TestBech32DecodeCacheSkipsLongStrings checks that the memo does not keep
// long (valid) bech32 strings, which untrusted txs can carry, so its memory
// stays bounded by bech32DecodeCacheMax * bech32DecodeCacheMaxLen.
func TestBech32DecodeCacheSkipsLongStrings(t *testing.T) {
	prev := ExecutionCachesEnabled()
	t.Cleanup(func() { SetExecutionCaches(prev); resetBech32Cache() })
	resetBech32Cache()
	SetExecutionCaches(true)

	long, err := bech32.ConvertAndEncode("cosmos", bytes.Repeat([]byte{1}, 600))
	require.NoError(t, err)
	require.Greater(t, len(long), bech32DecodeCacheMaxLen)
	bz, err := GetFromBech32(long, "cosmos")
	require.NoError(t, err)
	require.Len(t, bz, 600)
	require.Equal(t, 0, bech32CacheLen())
}

// TestBech32DecodeCacheConcurrent exercises the memo from many goroutines
// (CheckTx, queries and DeliverTx run concurrently at the tip); run with -race.
func TestBech32DecodeCacheConcurrent(t *testing.T) {
	prev := ExecutionCachesEnabled()
	t.Cleanup(func() { SetExecutionCaches(prev); resetBech32Cache() })
	resetBech32Cache()
	SetExecutionCaches(true)

	addrs := make([]string, 64)
	for i := range addrs {
		a, err := bech32.ConvertAndEncode("cosmos", bytes.Repeat([]byte{byte(i)}, 20))
		require.NoError(t, err)
		addrs[i] = a
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				a := addrs[(i*7+g)%len(addrs)]
				bz, err := GetFromBech32(a, "cosmos")
				if err != nil || len(bz) != 20 || bz[0] != byte((i*7+g)%len(addrs)) {
					t.Errorf("bad decode of %s: %x %v", a, bz, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}
