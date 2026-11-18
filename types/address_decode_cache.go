package types

import (
	"hash/maphash"
	"slices"
	"strings"
	"sync"

	"github.com/cosmos/cosmos-sdk/types/bech32"
)

// The cache is sharded so parallel callers do not serialize on one lock; a
// full shard is cleared.
// Strings longer than BIP-173's 90 characters are decoded but not cached.
const (
	bech32CacheShards   = 64
	bech32CacheShardMax = 1024
	bech32CacheMaxLen   = 90
)

type bech32Decoded struct {
	hrp string
	bz  []byte
}

type bech32CacheShard struct {
	mu sync.RWMutex
	m  map[string]bech32Decoded
}

var (
	bech32CacheSeed = maphash.MakeSeed()
	bech32Cache     [bech32CacheShards]bech32CacheShard
)

// decodeBech32Cached memoizes bech32.DecodeAndConvert, a pure function of the
// string: a BaseAccount stores its address as bech32, so every GetAddress
// decodes it. Each caller gets its own copy of the bytes.
func decodeBech32Cached(s string) (string, []byte, error) {
	if !IsAddrCacheEnabled() || len(s) > bech32CacheMaxLen {
		return bech32.DecodeAndConvert(s)
	}
	sh := &bech32Cache[maphash.String(bech32CacheSeed, s)%bech32CacheShards]
	sh.mu.RLock()
	d, ok := sh.m[s]
	sh.mu.RUnlock()
	if !ok {
		hrp, bz, err := bech32.DecodeAndConvert(s)
		if err != nil {
			return "", nil, err
		}
		d = bech32Decoded{hrp: hrp, bz: bz}
		sh.mu.Lock()
		if sh.m == nil {
			sh.m = make(map[string]bech32Decoded, bech32CacheShardMax)
		} else if len(sh.m) >= bech32CacheShardMax {
			clear(sh.m)
		}
		sh.m[strings.Clone(s)] = d
		sh.mu.Unlock()
	}
	return d.hrp, slices.Clone(d.bz), nil
}
