package keeper

import (
	"bytes"
	"sync"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// Execution cache (see sdk.ExecutionCachesEnabled):
// cache of decoded validators keyed by store key. The store read is always done
// by the caller (gas and store access are unchanged); a cached decode is used only
// if the raw bytes just read are byte-identical to the bytes it was decoded from,
// so the result equals a fresh decode. Every return is a deep copy (fresh big.Ints,
// fresh Any), so callers may mutate it freely. Only successful decodes are cached.
const celestiaValidatorCacheMax = 4096

type validatorCache struct {
	mu sync.Mutex
	m  map[string]validatorCacheEntry
}

type validatorCacheEntry struct {
	raw []byte
	val types.Validator
}

func newValidatorCache() *validatorCache {
	return &validatorCache{m: make(map[string]validatorCacheEntry)}
}

// unmarshalValidator decodes raw, the value stored under key, exactly like
// types.UnmarshalValidator.
func (k Keeper) unmarshalValidator(key, raw []byte) (types.Validator, error) {
	c := k.validatorCache
	if !sdk.ExecutionCachesEnabled() || c == nil {
		return types.UnmarshalValidator(k.cdc, raw)
	}

	c.mu.Lock()
	if e, ok := c.m[string(key)]; ok && bytes.Equal(e.raw, raw) {
		v := cloneValidator(e.val)
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()

	v, err := types.UnmarshalValidator(k.cdc, raw)
	if err != nil {
		return v, err
	}
	e := validatorCacheEntry{raw: append([]byte(nil), raw...), val: cloneValidator(v)}

	c.mu.Lock()
	if len(c.m) >= celestiaValidatorCacheMax {
		c.m = make(map[string]validatorCacheEntry)
	}
	c.m[string(key)] = e
	c.mu.Unlock()

	return v, nil
}

func cloneDec(d sdk.Dec) sdk.Dec {
	if d.IsNil() {
		return d
	}
	return d.Clone()
}

func cloneInt(i sdk.Int) sdk.Int {
	return sdk.NewIntFromBigInt(i.BigInt()) // BigInt copies; nil stays nil
}

// cloneValidator returns a deep copy of v sharing no mutable memory with it
// (strings and time.Time are immutable values; the unpacked pubkey is shared,
// see Any.CelestiaClone).
func cloneValidator(v types.Validator) types.Validator {
	c := v
	c.ConsensusPubkey = v.ConsensusPubkey.CelestiaClone()
	c.Tokens = cloneInt(v.Tokens)
	c.DelegatorShares = cloneDec(v.DelegatorShares)
	c.Commission.Rate = cloneDec(v.Commission.Rate)
	c.Commission.MaxRate = cloneDec(v.Commission.MaxRate)
	c.Commission.MaxChangeRate = cloneDec(v.Commission.MaxChangeRate)
	c.MinSelfDelegation = cloneInt(v.MinSelfDelegation)
	return c
}
