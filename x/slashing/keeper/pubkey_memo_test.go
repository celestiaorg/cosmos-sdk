package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/simapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/slashing/types"
)

// TestCheckPubkeyMatchesGetPubkey checks that the pubkey decode memo returns
// what GetPubkey would (nil or an error) and uses the same gas, with the
// execution caches on and off, for valid, missing and undecodable stored keys.
func TestCheckPubkeyMatchesGetPubkey(t *testing.T) {
	defer sdk.SetExecutionCaches(sdk.ExecutionCachesEnabled())
	app := simapp.Setup(t, false)
	ctx := app.BaseApp.NewContext(false, tmproto.Header{})
	k := app.SlashingKeeper
	store := ctx.KVStore(app.GetKey(types.StoreKey))

	valid := ed25519.GenPrivKeyFromSecret([]byte("valid")).PubKey()
	corrupt := ed25519.GenPrivKeyFromSecret([]byte("corrupt")).PubKey()
	missing := ed25519.GenPrivKeyFromSecret([]byte("missing")).PubKey()
	require.NoError(t, k.AddPubkey(ctx, valid))
	require.NoError(t, k.AddPubkey(ctx, corrupt))

	type result struct {
		err bool
		gas sdk.Gas
	}
	check := func(on bool) []result {
		sdk.SetExecutionCaches(on)
		var out []result
		for round := 0; round < 3; round++ { // later rounds hit the memo
			for _, pk := range []cryptotypes.PubKey{valid, corrupt, missing} {
				gctx := ctx.WithGasMeter(sdk.NewInfiniteGasMeter())
				err := k.CheckPubkey(gctx, pk.Address())
				_, stockErr := k.GetPubkey(ctx, pk.Address())
				require.Equal(t, stockErr != nil, err != nil, "on=%v round=%d", on, round)
				out = append(out, result{err: err != nil, gas: gctx.GasMeter().GasConsumed()})
			}
			if round == 0 {
				// overwrite a key that already decoded with bytes that do not
				store.Set(types.AddrPubkeyRelationKey(corrupt.Address()), []byte{0xff, 0x01, 0x02})
			}
		}
		require.NoError(t, k.AddPubkey(ctx, corrupt)) // restore for the next run
		return out
	}
	off := check(false)
	on := check(true)
	require.Equal(t, off, on)
	require.Equal(t, []bool{false, false, true, false, true, true}, []bool{on[0].err, on[1].err, on[2].err, on[3].err, on[4].err, on[5].err})
}
