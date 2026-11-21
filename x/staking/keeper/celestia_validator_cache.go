package keeper

import (
	"bytes"
	"sync"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// Sync-speedup prototype (env CELESTIA_VALIDATOR_CACHE=1 or CELESTIA_EXEC_CACHES=1):
// cache of decoded validators keyed by store key. The store read is always done
// by the caller (gas and store access are unchanged); a cached decode is used only
// if the raw bytes just read are byte-identical to the bytes it was decoded from,
// so the result equals a fresh decode. Every return is a deep copy (fresh big.Ints,
// fresh Any, fresh UnbondingIds), so callers may mutate it freely. Only successful
// decodes are cached.
//
// CELESTIA_PARAMS_CACHE: GetParams memoizes the params decode the same way.
const celestiaValidatorCacheMax = 4096

type celestiaCaches struct {
	mu         sync.Mutex
	validators map[string]validatorCacheEntry
	params     sdk.CelestiaDecodeMemo[types.Params]
}

type validatorCacheEntry struct {
	raw []byte
	val types.Validator
}

func newCelestiaCaches() *celestiaCaches {
	return &celestiaCaches{validators: make(map[string]validatorCacheEntry)}
}

// unmarshalValidator decodes raw, the value stored under key, exactly like
// types.UnmarshalValidator.
func (k Keeper) unmarshalValidator(key, raw []byte) (types.Validator, error) {
	c := k.celestiaCaches
	if !sdk.CelestiaValidatorCache || c == nil {
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
	if err != nil {
		return v, err
	}
	e := validatorCacheEntry{raw: append([]byte(nil), raw...), val: cloneValidator(v)}

	c.mu.Lock()
	if len(c.validators) >= celestiaValidatorCacheMax {
		c.validators = make(map[string]validatorCacheEntry)
	}
	c.validators[string(key)] = e
	c.mu.Unlock()

	return v, nil
}

// cloneValidator returns a deep copy of v sharing no mutable memory with it
// (strings and time.Time are immutable values; the unpacked pubkey is shared,
// see Any.CelestiaClone).
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
