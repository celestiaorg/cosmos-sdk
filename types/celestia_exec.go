package types

import "os"

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
