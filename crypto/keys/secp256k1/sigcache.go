package secp256k1

import (
	"crypto/sha256"
	"encoding/binary"
	"os"
	"sync"
)

// Sync-speedup prototype (env CELESTIA_SIG_PREVERIFY=1): a cache of
// (pubkey, message, signature) triples that have already been verified as
// VALID, e.g. by a parallel pre-verification pass over a block's txs. A cache
// hit returns true without recomputing; a miss falls through to the normal
// verification. Only successful verifications are ever inserted, so the
// accept/reject result for any input is unchanged.
var (
	SigCacheEnabled = os.Getenv("CELESTIA_SIG_PREVERIFY") == "1"
	sigCache        sync.Map // [32]byte -> struct{}
)

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

// SigCacheAdd records a triple that has been verified as valid.
func SigCacheAdd(pub, msg, sig []byte) { sigCache.Store(sigCacheKey(pub, msg, sig), struct{}{}) }

// SigCacheReset drops all cached entries.
func SigCacheReset() { sigCache.Range(func(k, _ any) bool { sigCache.Delete(k); return true }) }

func sigCacheConsume(pub, msg, sig []byte) bool {
	_, ok := sigCache.LoadAndDelete(sigCacheKey(pub, msg, sig))
	return ok
}
