package types

import (
	"bytes"
	"sync"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Sync-speedup prototype (env CELESTIA_PARAMS_CACHE=1 or CELESTIA_EXEC_CACHES=1):
// memo of amino-JSON param decodes keyed by (subspace name, key). The store read
// is always done by the caller (gas unchanged); a memoized value is used only if
// the raw bytes just read are byte-identical to the bytes it was decoded from and
// the destination has the same concrete type. Only a whitelist of value types is
// memoized (scalars, string, time.Duration, sdk.Dec); Dec is returned as a
// fresh copy (sdk.Int is excluded: its UnmarshalJSON writes into an existing
// big.Int, which a memo could not reproduce). Everything else takes the normal decode.
const celestiaParamsCacheMax = 4096

type paramsCacheEntry struct {
	raw []byte
	val interface{} // dereferenced value, private copy
}

var (
	paramsCacheMu sync.Mutex
	paramsCache   = make(map[string]paramsCacheEntry)
)

func paramsCacheKey(name, key []byte) string {
	return string(name) + "/" + string(key)
}

// celestiaCopyParam copies a whitelisted dereferenced value (deep for Dec).
func celestiaCopyParam(v interface{}) (interface{}, bool) {
	switch x := v.(type) {
	case bool, string, int64, uint64, int32, uint32, int, uint, int16, uint16, int8, uint8, time.Duration:
		return x, true
	case sdk.Dec:
		if x.IsNil() {
			return x, true
		}
		return x.Clone(), true
	}
	return nil, false
}

// celestiaDeref returns *ptr for whitelisted pointer types.
func celestiaDeref(ptr interface{}) (interface{}, bool) {
	switch p := ptr.(type) {
	case *bool:
		return *p, true
	case *string:
		return *p, true
	case *int64:
		return *p, true
	case *uint64:
		return *p, true
	case *int32:
		return *p, true
	case *uint32:
		return *p, true
	case *int:
		return *p, true
	case *uint:
		return *p, true
	case *int16:
		return *p, true
	case *uint16:
		return *p, true
	case *int8:
		return *p, true
	case *uint8:
		return *p, true
	case *time.Duration:
		return *p, true
	case *sdk.Dec:
		return *p, true
	}
	return nil, false
}

// celestiaAssign sets *ptr = v if the types match exactly.
func celestiaAssign(ptr, v interface{}) bool {
	switch p := ptr.(type) {
	case *bool:
		x, ok := v.(bool)
		if ok {
			*p = x
		}
		return ok
	case *string:
		x, ok := v.(string)
		if ok {
			*p = x
		}
		return ok
	case *int64:
		x, ok := v.(int64)
		if ok {
			*p = x
		}
		return ok
	case *uint64:
		x, ok := v.(uint64)
		if ok {
			*p = x
		}
		return ok
	case *int32:
		x, ok := v.(int32)
		if ok {
			*p = x
		}
		return ok
	case *uint32:
		x, ok := v.(uint32)
		if ok {
			*p = x
		}
		return ok
	case *int:
		x, ok := v.(int)
		if ok {
			*p = x
		}
		return ok
	case *uint:
		x, ok := v.(uint)
		if ok {
			*p = x
		}
		return ok
	case *int16:
		x, ok := v.(int16)
		if ok {
			*p = x
		}
		return ok
	case *uint16:
		x, ok := v.(uint16)
		if ok {
			*p = x
		}
		return ok
	case *int8:
		x, ok := v.(int8)
		if ok {
			*p = x
		}
		return ok
	case *uint8:
		x, ok := v.(uint8)
		if ok {
			*p = x
		}
		return ok
	case *time.Duration:
		x, ok := v.(time.Duration)
		if ok {
			*p = x
		}
		return ok
	case *sdk.Dec:
		x, ok := v.(sdk.Dec)
		if ok {
			*p = x
		}
		return ok
	}
	return false
}

// celestiaParamsCacheGet fills ptr from the memo if bz matches; it reports
// whether it did.
func (s Subspace) celestiaParamsCacheGet(key, bz []byte, ptr interface{}) bool {
	if !sdk.CelestiaParamsCache || bz == nil {
		return false
	}
	paramsCacheMu.Lock()
	e, ok := paramsCache[paramsCacheKey(s.name, key)]
	paramsCacheMu.Unlock()
	if !ok || !bytes.Equal(e.raw, bz) {
		return false
	}
	v, ok := celestiaCopyParam(e.val)
	if !ok {
		return false
	}
	return celestiaAssign(ptr, v)
}

// celestiaParamsCachePut memoizes the value just decoded from bz into ptr.
func (s Subspace) celestiaParamsCachePut(key, bz []byte, ptr interface{}) {
	if !sdk.CelestiaParamsCache || bz == nil {
		return
	}
	v, ok := celestiaDeref(ptr)
	if !ok {
		return
	}
	if v, ok = celestiaCopyParam(v); !ok {
		return
	}
	e := paramsCacheEntry{raw: append([]byte(nil), bz...), val: v}
	paramsCacheMu.Lock()
	if len(paramsCache) >= celestiaParamsCacheMax {
		paramsCache = make(map[string]paramsCacheEntry)
	}
	paramsCache[paramsCacheKey(s.name, key)] = e
	paramsCacheMu.Unlock()
}
