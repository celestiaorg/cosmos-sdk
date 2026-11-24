package types

import (
	"sync"
	"sync/atomic"
)

// Execution cache (see ExecutionCachesEnabled): memoize successful bech32
// decodes. Decoding is a pure function of the input string, so a cache
// hit returns exactly what DecodeAndConvert would (a copy, so callers may
// mutate it). Only successful decodes of strings up to bech32DecodeCacheMaxLen
// bytes are cached; the cache is reset when it exceeds bech32DecodeCacheMax
// entries, so it holds at most about 64 MiB.
var (
	bech32DecodeCache     sync.Map // string -> bech32DecodeEntry
	bech32DecodeCacheSize int64
)

const (
	bech32DecodeCacheMax = 1 << 18
	// bech32DecodeCacheMaxLen covers 20- and 32-byte addresses with any
	// realistic prefix. Longer (up to 1023-byte) strings are valid bech32 and
	// can come from untrusted txs, so they are not cached.
	bech32DecodeCacheMaxLen = 128
)

type bech32DecodeEntry struct {
	hrp string
	bz  []byte
}

func bech32DecodeCacheStore(s, hrp string, bz []byte) {
	if len(s) > bech32DecodeCacheMaxLen {
		return
	}
	if atomic.AddInt64(&bech32DecodeCacheSize, 1) > bech32DecodeCacheMax {
		bech32DecodeCache.Range(func(k, _ any) bool { bech32DecodeCache.Delete(k); return true })
		atomic.StoreInt64(&bech32DecodeCacheSize, 0)
	}
	bech32DecodeCache.Store(s, bech32DecodeEntry{hrp: hrp, bz: append([]byte(nil), bz...)})
}
