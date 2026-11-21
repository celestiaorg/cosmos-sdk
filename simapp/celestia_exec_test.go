package simapp

import (
	"encoding/json"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	abci "github.com/tendermint/tendermint/abci/types"
	tmed25519 "github.com/tendermint/tendermint/crypto/ed25519"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
	tmtypes "github.com/tendermint/tendermint/types"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/simapp/helpers"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

type celestiaBlockResult struct {
	appHash []byte
	txs     []abci.ResponseDeliverTx
}

// runCelestiaExecChain runs a deterministic multi-validator chain (delegations
// changing powers every block, missed signatures, historical-info pruning with
// HistoricalEntries=3) and returns per-block app hashes and DeliverTx results.
func runCelestiaExecChain(t *testing.T, flagsOn bool, nBlocks int) []celestiaBlockResult {
	defer sdk.CelestiaSetExecFlags(flagsOn)()

	const nVals = 6
	tmVals := make([]*tmtypes.Validator, nVals)
	for i := range tmVals {
		pk := tmed25519.GenPrivKeyFromSecret([]byte{byte(i), 'v'}).PubKey()
		tmVals[i] = tmtypes.NewValidator(pk, 1)
	}
	valSet := tmtypes.NewValidatorSet(tmVals)

	priv := secp256k1.GenPrivKeyFromSecret([]byte("celestia-exec-delegator"))
	addr := sdk.AccAddress(priv.PubKey().Address())
	acc := authtypes.NewBaseAccount(addr, priv.PubKey(), 0, 0)
	bal := banktypes.Balance{Address: addr.String(), Coins: sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, sdk.NewInt(1_000_000_000_000_000)))}
	app := celestiaSetupWithValSet(t, valSet, acc, bal)

	// still inside block 2 (begun by SetupWithGenesisValSet)
	ctx := app.BaseApp.NewContext(false, tmproto.Header{})
	sp := app.StakingKeeper.GetParams(ctx)
	sp.HistoricalEntries = 3
	app.StakingKeeper.SetParams(ctx, sp)
	for _, v := range app.StakingKeeper.GetAllValidators(ctx) {
		consAddr, err := v.GetConsAddr()
		require.NoError(t, err)
		app.SlashingKeeper.AddPubkey(ctx, mustConsPubKey(t, v))
		app.SlashingKeeper.SetValidatorSigningInfo(ctx, consAddr, slashingtypes.NewValidatorSigningInfo(consAddr, 0, 0, time.Unix(0, 0), false, 0))
	}
	app.EndBlock(abci.RequestEndBlock{Height: app.LastBlockHeight() + 1})
	app.Commit()

	enc := MakeTestEncodingConfig()
	rnd := rand.New(rand.NewSource(1))
	seq := uint64(0)
	genesis := time.Unix(1_700_000_000, 0).UTC()
	var out []celestiaBlockResult
	for b := 0; b < nBlocks; b++ {
		h := app.LastBlockHeight() + 1
		votes := make([]abci.VoteInfo, nVals)
		for i, v := range valSet.Validators {
			votes[i] = abci.VoteInfo{
				Validator:       abci.Validator{Address: v.Address, Power: 1},
				SignedLastBlock: !(i == 1 && b%3 == 0) && !(i == 4 && b%2 == 0),
			}
		}
		app.BeginBlock(abci.RequestBeginBlock{
			Header: tmproto.Header{
				Height: h, Time: genesis.Add(time.Duration(b) * 6 * time.Second),
				ProposerAddress: valSet.Validators[b%nVals].Address,
			},
			LastCommitInfo: abci.LastCommitInfo{Votes: votes},
		})

		var res celestiaBlockResult
		for k := 0; k < 2; k++ {
			valAddr := sdk.ValAddress(valSet.Validators[(b+k)%nVals].Address)
			var msg sdk.Msg = stakingtypes.NewMsgDelegate(addr, valAddr, sdk.NewCoin(sdk.DefaultBondDenom, sdk.DefaultPowerReduction.MulRaw(int64(1+(b*7+k)%5))))
			if k == 1 && b%2 == 1 {
				msg = banktypes.NewMsgSend(addr, sdk.AccAddress(valSet.Validators[0].Address), sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 10)))
			}
			tx, err := helpers.GenSignedMockTx(rnd, enc.TxConfig, []sdk.Msg{msg}, sdk.Coins{sdk.NewInt64Coin(sdk.DefaultBondDenom, 1)}, 400_000, "", []uint64{0}, []uint64{seq}, priv)
			require.NoError(t, err)
			bz, err := enc.TxConfig.TxEncoder()(tx)
			require.NoError(t, err)
			r := app.DeliverTx(abci.RequestDeliverTx{Tx: bz})
			require.Equal(t, uint32(0), r.Code, r.Log)
			seq++
			res.txs = append(res.txs, r)
		}
		app.EndBlock(abci.RequestEndBlock{Height: h})
		res.appHash = app.Commit().Data
		out = append(out, res)
	}
	return out
}

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

func TestCelestiaExecFlagsDeterminism(t *testing.T) {
	const n = 25
	off := runCelestiaExecChain(t, false, n)
	on := runCelestiaExecChain(t, true, n)
	require.Len(t, on, n)
	for b := range off {
		require.Equal(t, off[b].appHash, on[b].appHash, "app hash differs at block %d", b)
		require.Len(t, on[b].txs, len(off[b].txs))
		for i := range off[b].txs {
			x, y := off[b].txs[i], on[b].txs[i]
			require.Equal(t, x.Code, y.Code)
			require.Equal(t, x.Data, y.Data)
			require.Equal(t, x.GasWanted, y.GasWanted)
			require.Equal(t, x.GasUsed, y.GasUsed)
			require.NotEmpty(t, x.Log)
			require.Empty(t, y.Log) // events-off path exercised
			require.Equal(t, len(x.Events), len(y.Events))
		}
	}
}
