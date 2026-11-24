package keeper

import "github.com/cosmos/cosmos-sdk/x/staking/types"

// CelestiaValidatorCacheLen returns the number of cached decoded validators.
func (k Keeper) CelestiaValidatorCacheLen() int {
	k.celestiaCaches.mu.Lock()
	defer k.celestiaCaches.mu.Unlock()
	return len(k.celestiaCaches.validators)
}

// CelestiaValidatorCacheMaster returns the cache's private copy for key.
func (k Keeper) CelestiaValidatorCacheMaster(key []byte) (types.Validator, bool) {
	k.celestiaCaches.mu.Lock()
	defer k.celestiaCaches.mu.Unlock()
	e, ok := k.celestiaCaches.validators[string(key)]
	return e.val, ok
}
