package keeper

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// GetHistoricalInfo gets the historical info at a given height
func (k Keeper) GetHistoricalInfo(ctx context.Context, height int64) (types.HistoricalInfo, error) {
	store := k.storeService.OpenKVStore(ctx)
	key := types.GetHistoricalInfoKey(height)

	value, err := store.Get(key)
	if err != nil {
		return types.HistoricalInfo{}, err
	}

	if value == nil {
		return types.HistoricalInfo{}, types.ErrNoHistoricalInfo
	}

	return types.UnmarshalHistoricalInfo(k.cdc, value)
}

// SetHistoricalInfo sets the historical info at a given height
func (k Keeper) SetHistoricalInfo(ctx context.Context, height int64, hi *types.HistoricalInfo) error {
	store := k.storeService.OpenKVStore(ctx)
	key := types.GetHistoricalInfoKey(height)
	value, err := k.cdc.Marshal(hi)
	if err != nil {
		return err
	}
	return store.Set(key, value)
}

// DeleteHistoricalInfo deletes the historical info at a given height
func (k Keeper) DeleteHistoricalInfo(ctx context.Context, height int64) error {
	store := k.storeService.OpenKVStore(ctx)
	key := types.GetHistoricalInfoKey(height)

	return store.Delete(key)
}

// IterateHistoricalInfo provides an iterator over all stored HistoricalInfo
// objects. For each HistoricalInfo object, cb will be called. If the cb returns
// true, the iterator will break and close.
func (k Keeper) IterateHistoricalInfo(ctx context.Context, cb func(types.HistoricalInfo) bool) error {
	store := k.storeService.OpenKVStore(ctx)
	iterator, err := store.Iterator(types.HistoricalInfoKey, storetypes.PrefixEndBytes(types.HistoricalInfoKey))
	if err != nil {
		return err
	}
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		histInfo, err := types.UnmarshalHistoricalInfo(k.cdc, iterator.Value())
		if err != nil {
			return err
		}
		if cb(histInfo) {
			break
		}
	}

	return nil
}

// GetAllHistoricalInfo returns all stored HistoricalInfo objects.
func (k Keeper) GetAllHistoricalInfo(ctx context.Context) ([]types.HistoricalInfo, error) {
	var infos []types.HistoricalInfo
	err := k.IterateHistoricalInfo(ctx, func(histInfo types.HistoricalInfo) bool {
		infos = append(infos, histInfo)
		return false
	})

	return infos, err
}

// TrackHistoricalInfo saves the latest historical-info and deletes the oldest
// heights that are below pruning height
func (k Keeper) TrackHistoricalInfo(ctx context.Context) error {
	entryNum, err := k.HistoricalEntries(ctx)
	if err != nil {
		return err
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Prune store to ensure we only have parameter-defined historical entries.
	// In most cases, this will involve removing a single historical entry.
	// In the rare scenario when the historical entries gets reduced to a lower value k'
	// from the original value k. k - k' entries must be deleted from the store.
	// Since the entries to be deleted are always in a continuous range, we can iterate
	// over the historical entries starting from the most recent version to be pruned
	// and then return at the first empty entry.
	for i := sdkCtx.BlockHeight() - int64(entryNum); i >= 0; i-- {
		_, err := k.GetHistoricalInfo(ctx, i)
		if err != nil {
			if errors.Is(err, types.ErrNoHistoricalInfo) {
				break
			}
			return err
		}
		if err = k.DeleteHistoricalInfo(ctx, i); err != nil {
			return err
		}
	}

	// if there is no need to persist historicalInfo, return
	if entryNum == 0 {
		return nil
	}

	// Create HistoricalInfo struct
	lastVals, err := k.GetLastValidators(ctx)
	if err != nil {
		return err
	}

	historicalEntry := types.NewHistoricalInfo(sdkCtx.BlockHeader(), types.Validators{Validators: lastVals, ValidatorCodec: k.validatorAddressCodec}, k.PowerReduction(ctx))

	// Set latest HistoricalInfo at current height
	return k.SetHistoricalInfo(ctx, sdkCtx.BlockHeight(), &historicalEntry)
}

func (k Keeper) MigrateHistoricalInfoKeys(ctx sdk.Context, iterationLimit int) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	prefixStore := prefix.NewStore(store, types.HistoricalInfoKey)

	// Check the store to see if there is a value stored under the keySuffix
	keySuffix := store.Get(types.NextMigrateHistoricalInfoKey)
	if keySuffix == nil {
		return nil
	}

	iterationCounter := 0

	// Start the iterator from the key that is in the store
	iterator := prefixStore.Iterator(keySuffix, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		if iterationCounter >= iterationLimit {
			ctx.Logger().Info(fmt.Sprintf("Migrated %d historical info entries, next key %s", iterationLimit, iterator.Key()))

			// Set the key in the store after it has been processed
			store.Set(types.NextMigrateHistoricalInfoKey, keySuffix)
			break
		}

		strHeight := iterator.Key()

		intHeight, err := strconv.ParseInt(string(strHeight), 10, 64)
		if err != nil {
			return fmt.Errorf("can't parse height from key %q to int64: %v", strHeight, err)
		}

		newStoreKey := types.GetHistoricalInfoKey(intHeight)

		// Set new key on store. Values don't change.
		store.Set(newStoreKey, iterator.Value())
		prefixStore.Delete(iterator.Key())

		iterationCounter++
	}

	// If the iterator is invalid we have processed the full store
	if !iterator.Valid() {
		ctx.Logger().Info("Migration completed for historical info")
		store.Delete(types.NextMigrateHistoricalInfoKey)
	}

	return nil
}
