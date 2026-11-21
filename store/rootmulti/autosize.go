package rootmulti

import (
	"os"
	"strconv"
)

// effectiveIAVLCacheSize returns the per-store IAVL node cache size. When
// CELESTIA_IAVL_CACHE_NODES is set (e.g. by CELESTIA_SYNC_AUTO=1 machine
// auto-sizing, see cometbft-db), it is a TOTAL node budget shared evenly by the
// mounted IAVL stores; otherwise the configured iavl-cache-size is used.
// Cache size only affects memory/speed, never state or hashes.
func (rs *Store) effectiveIAVLCacheSize() int {
	total, err := strconv.Atoi(os.Getenv("CELESTIA_IAVL_CACHE_NODES"))
	if err != nil || total <= 0 {
		return rs.iavlCacheSize
	}
	n := len(rs.storesParams)
	if n < 1 {
		n = 1
	}
	per := total / n
	if per < 10000 {
		per = 10000
	}
	return per
}
