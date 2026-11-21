package types

import (
	"bytes"
	"os"
	"sync"

	"cosmossdk.io/math"
)

// Sync-speedup prototype: env-gated execution caches/shortcuts (see EXEC-CACHES.md).
// Every flag defaults to off (upstream behaviour). CELESTIA_EXEC_CACHES=1 turns on
// all of them; an individual variable set to "0" keeps that one off.
//
//	CELESTIA_VALIDATOR_CACHE      decoded staking Validator cache (raw-bytes checked, deep copies)
//	CELESTIA_PARAMS_CACHE         params decode memo (raw-bytes checked, copies)
//	CELESTIA_EXEC_SHORTCUTS       decode-free existence checks, sort key memo, GetOperator dedupe
//	CELESTIA_BLOCKSYNC_EVENTS_OFF no DeliverTx Log / index marking / DecCoins event strings in
//	                              deliver-mode responses (none of these are hashed)
var (
	CelestiaValidatorCache     = CelestiaExecFlag("CELESTIA_VALIDATOR_CACHE")
	CelestiaParamsCache        = CelestiaExecFlag("CELESTIA_PARAMS_CACHE")
	CelestiaExecShortcuts      = CelestiaExecFlag("CELESTIA_EXEC_SHORTCUTS")
	CelestiaBlocksyncEventsOff = CelestiaExecFlag("CELESTIA_BLOCKSYNC_EVENTS_OFF")
)

// CelestiaExecFlag reports whether the named speedup is enabled: name=1, or
// CELESTIA_EXEC_CACHES=1 unless name=0.
func CelestiaExecFlag(name string) bool {
	switch os.Getenv(name) {
	case "1":
		return true
	case "0":
		return false
	}
	return os.Getenv("CELESTIA_EXEC_CACHES") == "1"
}

// CelestiaSetExecFlags sets all execution speedup flags (tests only) and
// returns a function restoring the previous values.
func CelestiaSetExecFlags(on bool) (restore func()) {
	a, b, c, d := CelestiaValidatorCache, CelestiaParamsCache, CelestiaExecShortcuts, CelestiaBlocksyncEventsOff
	CelestiaValidatorCache, CelestiaParamsCache, CelestiaExecShortcuts, CelestiaBlocksyncEventsOff = on, on, on, on
	return func() {
		CelestiaValidatorCache, CelestiaParamsCache, CelestiaExecShortcuts, CelestiaBlocksyncEventsOff = a, b, c, d
	}
}

// CelestiaDecodeMemo memoizes the decode of a single store value (e.g. a
// module's params). Get returns a clone of the memoized value only if raw is
// byte-identical to the bytes it was decoded from. The caller always does the
// store read itself (gas unchanged) and must only Put successful decodes.
type CelestiaDecodeMemo[T any] struct {
	mu  sync.Mutex
	raw []byte
	val T
	ok  bool
}

// Get returns clone(memoized value) if raw matches.
func (m *CelestiaDecodeMemo[T]) Get(raw []byte, clone func(T) T) (T, bool) {
	var zero T
	if m == nil || raw == nil {
		return zero, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ok || !bytes.Equal(m.raw, raw) {
		return zero, false
	}
	return clone(m.val), true
}

// Put memoizes clone(v) as the decode of raw.
func (m *CelestiaDecodeMemo[T]) Put(raw []byte, v T, clone func(T) T) {
	if m == nil || raw == nil {
		return
	}
	c := clone(v)
	r := append([]byte(nil), raw...)
	m.mu.Lock()
	m.raw, m.val, m.ok = r, c, true
	m.mu.Unlock()
}

// CelestiaCloneLegacyDec returns a copy of d sharing no memory (nil stays nil).
func CelestiaCloneLegacyDec(d math.LegacyDec) math.LegacyDec {
	if d.IsNil() {
		return d
	}
	return d.Clone()
}

// CelestiaCloneInt returns a copy of i sharing no memory (nil stays nil).
func CelestiaCloneInt(i math.Int) math.Int {
	return math.NewIntFromBigInt(i.BigInt()) // BigInt copies
}
