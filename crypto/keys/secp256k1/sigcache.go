package secp256k1

import (
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"sync/atomic"
)

// A cache of (pubkey, message, signature) triples already verified as VALID by
// a parallel pre-verification pass over a block's txs. A hit returns true
// without recomputing; a miss verifies as usual. Only valid triples are
// inserted, so the result for any input is unchanged. It is enabled by
// [performance] signature-preverify (see EnableSigCache).
var (
	sigCacheEnabled atomic.Bool
	sigCache        sync.Map // [32]byte -> struct{}
)

// EnableSigCache turns the pre-verification cache on or off (process-wide).
func EnableSigCache(enabled bool) {
	sigCacheEnabled.Store(enabled)
	if !enabled {
		SigCacheReset()
	}
}

// SigCacheEnabled reports whether the pre-verification cache is on.
func SigCacheEnabled() bool { return sigCacheEnabled.Load() }

func sigCacheKey(pub, msg, sig []byte) [32]byte {
	h := sha256.New()
	var l [8]byte
	for _, b := range [][]byte{pub, msg, sig} {
		binary.BigEndian.PutUint64(l[:], uint64(len(b)))
		h.Write(l[:])
		h.Write(b)
	}
	var k [32]byte
	copy(k[:], h.Sum(nil))
	return k
}

// PreverifySignature verifies sig without consulting the cache and, if it is
// valid, caches the triple for one later VerifySignature call. Only this
// function inserts entries, so the cache can never hold an unverified triple.
func PreverifySignature(pub *PubKey, msg, sig []byte) bool {
	if !pub.verifySignatureUncached(msg, sig) {
		return false
	}
	sigCache.Store(sigCacheKey(pub.Key, msg, sig), struct{}{})
	return true
}

// SigCacheReset drops all cached entries.
func SigCacheReset() { sigCache.Range(func(k, _ any) bool { sigCache.Delete(k); return true }) }

func sigCacheConsume(pub, msg, sig []byte) bool {
	_, ok := sigCache.LoadAndDelete(sigCacheKey(pub, msg, sig))
	return ok
}

// SigCacheLen returns the number of cached triples (tests and metrics).
func SigCacheLen() int {
	n := 0
	sigCache.Range(func(_, _ any) bool { n++; return true })
	return n
}
