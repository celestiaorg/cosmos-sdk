package types

import (
	"sync"
	"sync/atomic"
)

// Sync-speedup prototype (env CELESTIA_BECH32_CACHE=1): memoize successful
// bech32 decodes. Decoding is a pure function of the input string, so a cache
// hit returns exactly what DecodeAndConvert would (a copy, so callers may
// mutate it). Only successful decodes are cached; the cache is reset when it
// exceeds a size bound.
var (
	bech32DecodeCacheEnabled = CelestiaExecFlag("CELESTIA_BECH32_CACHE") // also on with CELESTIA_EXEC_CACHES=1
	bech32DecodeCache        sync.Map                                    // string -> bech32DecodeEntry
	bech32DecodeCacheSize    int64
)

const bech32DecodeCacheMax = 1 << 18

type bech32DecodeEntry struct {
	hrp string
	bz  []byte
}

func bech32DecodeCacheStore(s, hrp string, bz []byte) {
	if atomic.AddInt64(&bech32DecodeCacheSize, 1) > bech32DecodeCacheMax {
		bech32DecodeCache.Range(func(k, _ any) bool { bech32DecodeCache.Delete(k); return true })
		atomic.StoreInt64(&bech32DecodeCacheSize, 0)
	}
	bech32DecodeCache.Store(s, bech32DecodeEntry{hrp: hrp, bz: append([]byte(nil), bz...)})
}
