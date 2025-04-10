package v5

import (
	"context"
	"fmt"
	"strconv"

	"cosmossdk.io/log"
	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

func migrateDelegationsByValidatorIndex(ctx sdk.Context, store storetypes.KVStore, cdc codec.BinaryCodec) error {
	iterator := storetypes.KVStorePrefixIterator(store, DelegationKey)

	for ; iterator.Valid(); iterator.Next() {
		key := iterator.Key()
		del, val, err := ParseDelegationKey(key)
		if err != nil {
			return err
		}

		store.Set(GetDelegationsByValKey(val, del), []byte{})
	}

	return nil
}

// MigrateStore performs in-place store migrations from v4 to v5.
func MigrateStore(ctx sdk.Context, store storetypes.KVStore, cdc codec.BinaryCodec) error {
	if err := migrateDelegationsByValidatorIndex(ctx, store, cdc); err != nil {
		return err
	}
	return migrateHistoricalInfoKeys(store, ctx.Logger())
}

// migrateHistoricalInfoKeys migrate HistoricalInfo keys to binary format
func migrateHistoricalInfoKeys(store storetypes.KVStore, logger log.Logger) error {
	// old key is of format:
	// prefix (0x50) || heightBytes (string representation of height in 10 base)
	// new key is of format:
	// prefix (0x50) || heightBytes (byte array representation using big-endian byte order)
	oldStore := prefix.NewStore(store, HistoricalInfoKey)

	oldStoreIter := oldStore.Iterator(nil, nil)
	defer sdk.LogDeferred(logger, func() error { return oldStoreIter.Close() })

	for ; oldStoreIter.Valid(); oldStoreIter.Next() {
		strHeight := oldStoreIter.Key()

		intHeight, err := strconv.ParseInt(string(strHeight), 10, 64)
		if err != nil {
			return fmt.Errorf("can't parse height from key %q to int64: %v", strHeight, err)
		}

		newStoreKey := GetHistoricalInfoKey(intHeight)

		// Set new key on store. Values don't change.
		store.Set(newStoreKey, oldStoreIter.Value())
		oldStore.Delete(oldStoreIter.Key())
	}

	return nil
}

// migrateParams will set the params to store from legacySubspace
func migrateParams(ctx sdk.Context, store storetypes.KVStore, cdc codec.BinaryCodec) error {

	// Get the params from the store
	params, err := GetParams(ctx, store, cdc)
	if err != nil {
		return err
	}

	params.MaxCommissionRate = types.DefaultMaxCommissionRate

	// Set the params in the store
	if err := SetParams(ctx, params, store, cdc); err != nil {
		return err
	}

	return nil
}

// SetParams sets the x/staking module parameters.
// CONTRACT: This method performs no validation of the parameters.
func SetParams(ctx context.Context, params types.Params, store storetypes.KVStore, cdc codec.BinaryCodec) error {
	bz, err := cdc.Marshal(&params)
	if err != nil {
		return err
	}
	// Set the params in the store
	store.Set(types.ParamsKey, bz)

	return nil
}

// GetParams gets the x/staking module parameters.
func GetParams(ctx context.Context, store storetypes.KVStore, cdc codec.BinaryCodec) (params types.Params, err error) {
	bz := store.Get(types.ParamsKey)

	if bz == nil {
		return params, nil
	}

	err = cdc.Unmarshal(bz, &params)
	return params, err
}
