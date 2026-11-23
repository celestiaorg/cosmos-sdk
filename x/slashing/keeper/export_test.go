package keeper

import (
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// CheckPubkey exposes checkPubkey to tests.
func (k Keeper) CheckPubkey(ctx sdk.Context, a cryptotypes.Address) error {
	return k.checkPubkey(ctx, a)
}
