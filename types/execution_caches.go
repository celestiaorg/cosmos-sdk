package types

import "sync/atomic"

// executionCachesOff is false by default, so the execution caches are on.
var executionCachesOff atomic.Bool

// ExecutionCachesEnabled reports whether the execution caches are on. They
// memoize decodes after the store read, so gas and results never change.
func ExecutionCachesEnabled() bool { return !executionCachesOff.Load() }

// SetExecutionCaches turns the execution caches on or off for the process.
// Nodes set it at startup from app.toml [performance] execution-caches.
func SetExecutionCaches(enabled bool) { executionCachesOff.Store(!enabled) }
