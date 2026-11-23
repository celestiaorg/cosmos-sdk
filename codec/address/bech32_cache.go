package address

import (
	"encoding/binary"
	"hash/maphash"
	"strings"
	"sync"
)

// Bech32Codec memoizes its conversions, both pure functions of their input, in
// sharded caches like the GetFromBech32 decode cache: a full shard is cleared,
// strings longer than BIP-173's 90 characters are not cached, and
// sdk.SetAddrCacheEnabled(false) turns them off.
const (
	codecCacheShards   = 64
	codecCacheShardMax = 1024
	codecCacheMaxLen   = 90
	codecCacheMaxBytes = 64 // longer byte strings always encode to more than 90 chars
)

type bech32Decoded struct {
	hrp string
	bz  []byte
}

type codecCacheShard[V any] struct {
	mu sync.RWMutex
	m  map[string]V
}

type codecCache[V any] [codecCacheShards]codecCacheShard[V]

var (
	codecCacheSeed = maphash.MakeSeed()
	decodeCache    codecCache[bech32Decoded] // text -> decoded
	encodeCache    codecCache[string]        // uvarint(len(prefix)) + prefix + bytes -> text
)

func (c *codecCache[V]) get(k string) (V, bool) {
	sh := &c[maphash.String(codecCacheSeed, k)%codecCacheShards]
	sh.mu.RLock()
	v, ok := sh.m[k]
	sh.mu.RUnlock()
	return v, ok
}

func (c *codecCache[V]) put(k string, v V) {
	sh := &c[maphash.String(codecCacheSeed, k)%codecCacheShards]
	sh.mu.Lock()
	if sh.m == nil {
		sh.m = make(map[string]V, codecCacheShardMax)
	} else if len(sh.m) >= codecCacheShardMax {
		clear(sh.m)
	}
	sh.m[strings.Clone(k)] = v
	sh.mu.Unlock()
}

// encodeCacheKey length-prefixes the hrp so no two (prefix, bytes) pairs share a key.
func encodeCacheKey(prefix string, bz []byte) string {
	k := make([]byte, 0, binary.MaxVarintLen64+len(prefix)+len(bz))
	k = binary.AppendUvarint(k, uint64(len(prefix)))
	k = append(k, prefix...)
	return string(append(k, bz...))
}
