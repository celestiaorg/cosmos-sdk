package keeper

import (
	"hash/maphash"
	"slices"
	"sync"

	collcodec "cosmossdk.io/collections/codec"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/types"
)

// The cache is sharded so parallel callers do not serialize on one lock; a
// full shard is cleared.
const (
	accountCacheShards   = 64
	accountCacheShardMax = 256
)

// accountCodec memoizes account decoding by the stored bytes: a tx decodes
// its signer's account several times. Each caller gets its own copy.
type accountCodec struct {
	collcodec.ValueCodec[sdk.AccountI]
	seed   maphash.Seed
	shards *[accountCacheShards]accountCacheShard
}

type accountCacheShard struct {
	mu sync.RWMutex
	m  map[string]sdk.AccountI
}

func newAccountCodec(inner collcodec.ValueCodec[sdk.AccountI]) accountCodec {
	return accountCodec{ValueCodec: inner, seed: maphash.MakeSeed(), shards: new([accountCacheShards]accountCacheShard)}
}

func (c accountCodec) Decode(b []byte) (sdk.AccountI, error) {
	sh := &c.shards[maphash.Bytes(c.seed, b)%accountCacheShards]
	sh.mu.RLock()
	cached, ok := sh.m[string(b)]
	sh.mu.RUnlock()
	if ok {
		acc, _ := cloneAccount(cached)
		return acc, nil
	}
	acc, err := c.ValueCodec.Decode(b)
	if err != nil {
		return acc, err
	}
	if cached, ok := cloneAccount(acc); ok {
		sh.mu.Lock()
		if sh.m == nil {
			sh.m = make(map[string]sdk.AccountI, accountCacheShardMax)
		} else if len(sh.m) >= accountCacheShardMax {
			clear(sh.m)
		}
		sh.m[string(b)] = cached
		sh.mu.Unlock()
	}
	return acc, nil
}

// cloneAccount copies acc so that no setter on the copy reaches acc. It
// returns false for the account types it does not cache.
func cloneAccount(acc sdk.AccountI) (sdk.AccountI, bool) {
	switch a := acc.(type) {
	case *types.BaseAccount:
		if c, ok := cloneBaseAccount(a); ok {
			return c, true
		}
	case *types.ModuleAccount:
		if base, ok := cloneBaseAccount(a.BaseAccount); ok {
			c := *a
			c.BaseAccount = base
			c.Permissions = slices.Clone(c.Permissions)
			return &c, true
		}
	}
	return nil, false
}

// The decoded pubkey is shared, so only key types that never write to
// themselves are cached (multisig and secp256r1 do).
func cloneBaseAccount(a *types.BaseAccount) (*types.BaseAccount, bool) {
	if a == nil {
		return nil, false
	}
	c := *a
	if a.PubKey != nil {
		switch a.PubKey.GetCachedValue().(type) {
		case *secp256k1.PubKey, *ed25519.PubKey:
		default:
			return nil, false
		}
		pk := *a.PubKey
		pk.Value = slices.Clone(pk.Value)
		c.PubKey = &pk
	}
	return &c, true
}
