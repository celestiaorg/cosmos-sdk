package address

import (
	"sync"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Sync-speedup prototype (env CELESTIA_BECH32_CACHE=1 or CELESTIA_EXEC_CACHES=1):
// memo of Bech32Codec conversions. Both directions are pure functions of their
// input (and the prefix); only successful conversions are cached, byte slices
// are copied in and out, and the memo is reset when it exceeds a size bound.
// StringToBytes still runs the hrp check and VerifyAddressFormat on every call.
var celestiaBech32CodecCacheEnabled = sdk.CelestiaExecFlag("CELESTIA_BECH32_CACHE")

const celestiaBech32CodecCacheMax = 1 << 18

type celestiaBech32Decoded struct {
	hrp string
	bz  []byte
}

var celestiaBech32CodecCache = struct {
	mu  sync.RWMutex
	n   int
	dec map[string]celestiaBech32Decoded // text -> decoded
	enc map[string]map[string]string     // prefix -> string(bytes) -> text
}{
	dec: make(map[string]celestiaBech32Decoded),
	enc: make(map[string]map[string]string),
}

func celestiaBech32Grow() {
	c := &celestiaBech32CodecCache
	c.n++
	if c.n > celestiaBech32CodecCacheMax {
		c.n = 0
		c.dec = make(map[string]celestiaBech32Decoded)
		c.enc = make(map[string]map[string]string)
	}
}

func celestiaBech32LookupDecode(text string) (string, []byte, bool) {
	c := &celestiaBech32CodecCache
	c.mu.RLock()
	e, ok := c.dec[text]
	c.mu.RUnlock()
	if !ok {
		return "", nil, false
	}
	return e.hrp, append([]byte(nil), e.bz...), true
}

func celestiaBech32StoreDecode(text, hrp string, bz []byte) {
	e := celestiaBech32Decoded{hrp: hrp, bz: append([]byte(nil), bz...)}
	c := &celestiaBech32CodecCache
	c.mu.Lock()
	celestiaBech32Grow()
	c.dec[text] = e
	c.mu.Unlock()
}

func celestiaBech32LookupEncode(prefix string, bz []byte) (string, bool) {
	c := &celestiaBech32CodecCache
	c.mu.RLock()
	text, ok := c.enc[prefix][string(bz)]
	c.mu.RUnlock()
	return text, ok
}

func celestiaBech32StoreEncode(prefix string, bz []byte, text string) {
	c := &celestiaBech32CodecCache
	c.mu.Lock()
	celestiaBech32Grow()
	m := c.enc[prefix]
	if m == nil {
		m = make(map[string]string)
		c.enc[prefix] = m
	}
	m[string(bz)] = text
	c.mu.Unlock()
}
