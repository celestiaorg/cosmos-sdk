package keeper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"sync"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// celestiaValidatorCacheMax and celestiaHistoricalCacheMax bound the two
// caches below; a full cache is cleared.
const (
	celestiaValidatorCacheMax  = 4096    // decoded validators
	celestiaHistoricalCacheMax = 1 << 16 // historical-info hashes, keyed by height
)

// celestiaCaches holds decoded validators by store key, and the decoded
// params. An entry is used only when the bytes just read from the store are
// identical to the bytes it was decoded from, so results and gas are unchanged.
type celestiaCaches struct {
	mu         sync.Mutex
	validators map[string]validatorCacheEntry
	params     sdk.CelestiaDecodeMemo[types.Params]

	histMu     sync.Mutex
	historical map[int64][sha256.Size]byte // height -> hash of the HistoricalInfo this process encoded
}

type validatorCacheEntry struct {
	raw []byte
	val types.Validator
}

func newCelestiaCaches() *celestiaCaches {
	return &celestiaCaches{
		validators: make(map[string]validatorCacheEntry),
		historical: make(map[int64][sha256.Size]byte),
	}
}

// unmarshalValidator decodes raw, the value stored under key, exactly like
// types.UnmarshalValidator. Every return is a deep copy.
func (k Keeper) unmarshalValidator(key, raw []byte) (types.Validator, error) {
	c := k.celestiaCaches
	if !sdk.ExecutionCachesEnabled() || c == nil {
		return types.UnmarshalValidator(k.cdc, raw)
	}

	c.mu.Lock()
	if e, ok := c.validators[string(key)]; ok && bytes.Equal(e.raw, raw) {
		v := cloneValidator(e.val)
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()

	v, err := types.UnmarshalValidator(k.cdc, raw)
	if err != nil || !cacheableConsensusKey(v) {
		return v, err
	}
	e := validatorCacheEntry{raw: append([]byte(nil), raw...), val: cloneValidator(v)}

	c.mu.Lock()
	if len(c.validators) >= celestiaValidatorCacheMax {
		clear(c.validators)
	}
	c.validators[string(key)] = e
	c.mu.Unlock()

	return v, nil
}

// cacheableConsensusKey reports whether v's unpacked consensus key is of a type
// that never writes to itself, since copies of a cached validator share it.
func cacheableConsensusKey(v types.Validator) bool {
	if v.ConsensusPubkey == nil {
		return false
	}
	switch v.ConsensusPubkey.GetCachedValue().(type) {
	case *ed25519.PubKey, *secp256k1.PubKey:
		return true
	}
	return false
}

// cloneValidator returns a deep copy of v. Strings and time.Time are
// immutable; the unpacked consensus key is shared (see cacheableConsensusKey).
func cloneValidator(v types.Validator) types.Validator {
	c := v
	c.ConsensusPubkey = v.ConsensusPubkey.CelestiaClone()
	c.Tokens = sdk.CelestiaCloneInt(v.Tokens)
	c.DelegatorShares = sdk.CelestiaCloneLegacyDec(v.DelegatorShares)
	c.Commission.Rate = sdk.CelestiaCloneLegacyDec(v.Commission.Rate)
	c.Commission.MaxRate = sdk.CelestiaCloneLegacyDec(v.Commission.MaxRate)
	c.Commission.MaxChangeRate = sdk.CelestiaCloneLegacyDec(v.Commission.MaxChangeRate)
	c.MinSelfDelegation = sdk.CelestiaCloneInt(v.MinSelfDelegation)
	if v.UnbondingIds != nil {
		c.UnbondingIds = append(make([]uint64, 0, len(v.UnbondingIds)), v.UnbondingIds...)
	}
	return c
}

func cloneParams(p types.Params) types.Params {
	c := p
	c.MinCommissionRate = sdk.CelestiaCloneLegacyDec(p.MinCommissionRate)
	return c
}

// recordHistoricalInfo remembers the hash of the HistoricalInfo encoding this
// process wrote at height.
func (c *celestiaCaches) recordHistoricalInfo(height int64, value []byte) {
	if c == nil || !sdk.ExecutionCachesEnabled() {
		return
	}
	h := sha256.Sum256(value)
	c.histMu.Lock()
	if len(c.historical) >= celestiaHistoricalCacheMax {
		clear(c.historical)
	}
	c.historical[height] = h
	c.histMu.Unlock()
}

// checkHistoricalInfo returns the error GetHistoricalInfo would return. It
// does the same store read, and skips the decode only for bytes this process
// encoded itself (which decode); anything else is decoded as usual.
func (k Keeper) checkHistoricalInfo(ctx context.Context, height int64) error {
	c := k.celestiaCaches
	if c == nil || !sdk.ExecutionCachesEnabled() {
		_, err := k.GetHistoricalInfo(ctx, height)
		return err
	}
	value, err := k.storeService.OpenKVStore(ctx).Get(types.GetHistoricalInfoKey(height))
	if err != nil {
		return err
	}
	if value == nil {
		return types.ErrNoHistoricalInfo
	}
	c.histMu.Lock()
	want, ok := c.historical[height]
	if ok {
		delete(c.historical, height) // the caller prunes it
	}
	c.histMu.Unlock()
	if ok && want == sha256.Sum256(value) {
		return nil
	}
	_, err = types.UnmarshalHistoricalInfo(k.cdc, value)
	return err
}
