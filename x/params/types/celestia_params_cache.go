package types

import (
	"bytes"
	"reflect"
	"sync"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Execution cache (see sdk.ExecutionCachesEnabled):
// memo of amino-JSON param decodes keyed by (subspace name, key). The store read
// is always done by the caller (gas unchanged); a memoized value is used only if
// the raw bytes just read are byte-identical to the bytes it was decoded from and
// the destination has the same type. Only scalars (bool, string, integer kinds
// such as time.Duration) and sdk.Dec are memoized; Dec is returned as a
// fresh copy (sdk.Int is excluded: its UnmarshalJSON writes into an existing
// big.Int, which a memo could not reproduce). Everything else takes the normal decode.
const celestiaParamsCacheMax = 4096

type paramsCacheEntry struct {
	raw []byte
	val reflect.Value // private copy of the decoded value
}

var (
	paramsCacheMu sync.Mutex
	paramsCache   = make(map[string]paramsCacheEntry)

	decType = reflect.TypeOf(sdk.Dec{})
)

func paramsCacheKey(name, key []byte) string {
	return string(name) + "/" + string(key)
}

// memoizable reports whether ptr points to a value the memo can copy
// exactly: a scalar (bool, string, integer kinds such as time.Duration) or
// an sdk.Dec, which is cloned.
func memoizable(ptr interface{}) (reflect.Value, bool) {
	rv := reflect.ValueOf(ptr)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return reflect.Value{}, false
	}
	t := rv.Elem().Type()
	if t == decType {
		return rv.Elem(), true
	}
	switch t.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Elem(), true
	}
	return reflect.Value{}, false
}

// copyParam returns a copy of v sharing no memory with it.
func copyParam(v reflect.Value) reflect.Value {
	if d, ok := v.Interface().(sdk.Dec); ok {
		if d.IsNil() {
			return reflect.ValueOf(d)
		}
		return reflect.ValueOf(d.Clone())
	}
	return reflect.ValueOf(v.Interface())
}

// celestiaParamsCacheGet fills ptr from the memo if bz matches; it reports
// whether it did.
func (s Subspace) celestiaParamsCacheGet(key, bz []byte, ptr interface{}) bool {
	if !sdk.ExecutionCachesEnabled() || bz == nil {
		return false
	}
	dst, ok := memoizable(ptr)
	if !ok {
		return false
	}
	paramsCacheMu.Lock()
	e, ok := paramsCache[paramsCacheKey(s.name, key)]
	paramsCacheMu.Unlock()
	if !ok || e.val.Type() != dst.Type() || !bytes.Equal(e.raw, bz) {
		return false
	}
	dst.Set(copyParam(e.val))
	return true
}

// celestiaParamsCachePut memoizes the value just decoded from bz into ptr.
func (s Subspace) celestiaParamsCachePut(key, bz []byte, ptr interface{}) {
	if !sdk.ExecutionCachesEnabled() || bz == nil {
		return
	}
	src, ok := memoizable(ptr)
	if !ok {
		return
	}
	e := paramsCacheEntry{raw: append([]byte(nil), bz...), val: copyParam(src)}
	paramsCacheMu.Lock()
	if len(paramsCache) >= celestiaParamsCacheMax {
		paramsCache = make(map[string]paramsCacheEntry)
	}
	paramsCache[paramsCacheKey(s.name, key)] = e
	paramsCacheMu.Unlock()
}
