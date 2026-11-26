package types

import (
	"bytes"
	"sync"
	"sync/atomic"

	"cosmossdk.io/math"
)

// executionCachesOff turns off the execution caches (app.toml
// [performance] execution-caches = false). They are on by default.
var executionCachesOff atomic.Bool

// SetExecutionCaches turns the execution caches on or off. The caches never
// change gas, events or results; the switch is there in case one misbehaves.
func SetExecutionCaches(on bool) { executionCachesOff.Store(!on) }

// ExecutionCachesEnabled reports whether the execution caches are on.
func ExecutionCachesEnabled() bool { return !executionCachesOff.Load() }

// CelestiaDecodeMemo memoizes the decode of a single store value (e.g. a
// module's params). The caller always does the store read itself, so gas is
// unchanged, and only Puts successful decodes.
type CelestiaDecodeMemo[T any] struct {
	mu  sync.Mutex
	raw []byte
	val T
	ok  bool
}

// Get returns clone(memoized value) if raw is byte-identical to the memoized bytes.
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
