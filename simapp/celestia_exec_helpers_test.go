package simapp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	abci "github.com/tendermint/tendermint/abci/types"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
	tmtypes "github.com/tendermint/tendermint/types"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// celestiaSetupWithValSet is SetupWithGenesisValSet with the bonded pool
// funded for every validator (the stock helper funds one).
func celestiaSetupWithValSet(t *testing.T, valSet *tmtypes.ValidatorSet, acc authtypes.GenesisAccount, bal banktypes.Balance) *SimApp {
	app, genesisState := setup(true, 5)
	genesisState = genesisStateWithValSet(t, app, genesisState, valSet, []authtypes.GenesisAccount{acc}, bal)
	// the helper counts every delegation in the supply but funds the bonded
	// pool for one validator only: fund it for all
	var bankGen banktypes.GenesisState
	app.AppCodec().MustUnmarshalJSON(genesisState[banktypes.ModuleName], &bankGen)
	pool := authtypes.NewModuleAddress(stakingtypes.BondedPoolName).String()
	for i := range bankGen.Balances {
		if bankGen.Balances[i].Address == pool {
			bankGen.Balances[i].Coins = sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, sdk.DefaultPowerReduction.MulRaw(int64(len(valSet.Validators)))))
		}
	}
	genesisState[banktypes.ModuleName] = app.AppCodec().MustMarshalJSON(&bankGen)

	stateBytes, err := json.MarshalIndent(genesisState, "", " ")
	require.NoError(t, err)
	app.InitChain(abci.RequestInitChain{Validators: []abci.ValidatorUpdate{}, ConsensusParams: DefaultConsensusParams, AppStateBytes: stateBytes})
	app.Commit()
	app.BeginBlock(abci.RequestBeginBlock{Header: tmproto.Header{Height: app.LastBlockHeight() + 1}})
	return app
}

func mustConsPubKey(t *testing.T, v stakingtypes.Validator) cryptotypes.PubKey {
	pk, err := v.ConsPubKey()
	require.NoError(t, err)
	return pk
}
