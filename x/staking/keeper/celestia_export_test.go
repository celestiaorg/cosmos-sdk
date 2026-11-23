package keeper

import "github.com/cosmos/cosmos-sdk/x/staking/types"

// CelestiaValidatorCacheLen returns the number of cached decoded validators.
func (k Keeper) CelestiaValidatorCacheLen() int {
	k.validatorCache.mu.Lock()
	defer k.validatorCache.mu.Unlock()
	return len(k.validatorCache.m)
}

// CelestiaValidatorCacheMaster returns the cache's private copy for key.
func (k Keeper) CelestiaValidatorCacheMaster(key []byte) (types.Validator, bool) {
	k.validatorCache.mu.Lock()
	defer k.validatorCache.mu.Unlock()
	e, ok := k.validatorCache.m[string(key)]
	return e.val, ok
}
