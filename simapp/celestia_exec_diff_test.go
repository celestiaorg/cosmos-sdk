package simapp

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	abci "github.com/tendermint/tendermint/abci/types"
	tmed25519 "github.com/tendermint/tendermint/crypto/ed25519"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
	tmtypes "github.com/tendermint/tendermint/types"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/simapp/helpers"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// celestiaDiffTrace is everything a node exposes for a block: ABCI responses
// (CheckTx and DeliverTx in full, including events, logs and gas), the app
// hash, and what a concurrent query saw.
type celestiaDiffTrace struct {
	begin   abci.ResponseBeginBlock
	check   []abci.ResponseCheckTx
	deliver []abci.ResponseDeliverTx
	end     abci.ResponseEndBlock
	appHash []byte
	query   string
}

// runCelestiaDiffChain runs a seeded chain that exercises out-of-gas txs at
// many gas limits (in the ante handler and inside message execution), failing
// messages, param changes (also inside a discarded cache context), jailing,
// undelegations and redelegations, with CheckTx before every DeliverTx and a
// query reading validators and params concurrently with block execution.
// The execution caches are set to `on`; all responses must be byte-identical
// between the two settings.
func runCelestiaDiffChain(t *testing.T, on bool, nBlocks int) (out []celestiaDiffTrace, oogInAnte, oogInMsg int) {
	defer sdk.SetExecutionCaches(sdk.ExecutionCachesEnabled())
	sdk.SetExecutionCaches(on)

	const nVals = 6
	tmVals := make([]*tmtypes.Validator, nVals)
	for i := range tmVals {
		tmVals[i] = tmtypes.NewValidator(tmed25519.GenPrivKeyFromSecret([]byte{byte(i), 'd'}).PubKey(), 1)
	}
	valSet := tmtypes.NewValidatorSet(tmVals)
	valAddr := func(i int) sdk.ValAddress { return sdk.ValAddress(valSet.Validators[i%nVals].Address) }

	priv := secp256k1.GenPrivKeyFromSecret([]byte("celestia-exec-diff"))
	addr := sdk.AccAddress(priv.PubKey().Address())
	acc := authtypes.NewBaseAccount(addr, priv.PubKey(), 0, 0)
	bal := banktypes.Balance{Address: addr.String(), Coins: sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, sdk.NewInt(1_000_000_000_000_000)))}
	app := celestiaSetupWithValSet(t, valSet, acc, bal)

	ctx := app.BaseApp.NewContext(false, tmproto.Header{})
	sp := app.StakingKeeper.GetParams(ctx)
	sp.HistoricalEntries = 3
	app.StakingKeeper.SetParams(ctx, sp)
	slp := app.SlashingKeeper.GetParams(ctx)
	slp.SignedBlocksWindow = 10
	slp.MinSignedPerWindow = sdk.NewDecWithPrec(5, 1)
	slp.DowntimeJailDuration = time.Minute
	app.SlashingKeeper.SetParams(ctx, slp)
	for _, v := range app.StakingKeeper.GetAllValidators(ctx) {
		consAddr, err := v.GetConsAddr()
		require.NoError(t, err)
		app.SlashingKeeper.AddPubkey(ctx, mustConsPubKey(t, v))
		app.SlashingKeeper.SetValidatorSigningInfo(ctx, consAddr, slashingtypes.NewValidatorSigningInfo(consAddr, 0, 0, time.Unix(0, 0), false, 0))
	}
	app.EndBlock(abci.RequestEndBlock{Height: app.LastBlockHeight() + 1})
	app.Commit()

	enc := MakeTestEncodingConfig()
	rnd := rand.New(rand.NewSource(7))
	bond := func(n int64) sdk.Coin { return sdk.NewCoin(sdk.DefaultBondDenom, sdk.DefaultPowerReduction.MulRaw(n)) }
	genesis := time.Unix(1_700_000_000, 0).UTC()
	fullGas := uint64(0) // gas used by a plain delegation, measured in block 0

	for b := 0; b < nBlocks; b++ {
		h := app.LastBlockHeight() + 1
		header := tmproto.Header{Height: h, Time: genesis.Add(time.Duration(b) * 6 * time.Second), ProposerAddress: valSet.Validators[b%nVals].Address}

		// a query over the last committed state, running while the block executes
		qms, err := app.CommitMultiStore().CacheMultiStoreWithVersion(h - 1)
		require.NoError(t, err)
		qctx := sdk.NewContext(qms, header, true, app.Logger())
		queryDone := make(chan string)
		go func() {
			var sb strings.Builder
			for _, v := range app.StakingKeeper.GetAllValidators(qctx) {
				op, err := sdk.ValAddressFromBech32(v.OperatorAddress)
				fmt.Fprintf(&sb, "%x/%s/%s/%v/%v;", op, v.Tokens, v.DelegatorShares, v.Jailed, err)
			}
			queryDone <- sb.String()
		}()
		// Params are read on this goroutine: stock Subspace.kvStore appends to the
		// shared name slice, a pre-existing data race under concurrent reads.
		pms, err := app.CommitMultiStore().CacheMultiStoreWithVersion(h - 1)
		require.NoError(t, err)
		pctx := sdk.NewContext(pms, header, true, app.Logger())
		qparams := fmt.Sprintf("%v|%v|%v", app.StakingKeeper.GetParams(pctx), app.SlashingKeeper.GetParams(pctx), app.DistrKeeper.GetParams(pctx))

		votes := make([]abci.VoteInfo, nVals)
		for i, v := range valSet.Validators {
			votes[i] = abci.VoteInfo{
				Validator:       abci.Validator{Address: v.Address, Power: 1},
				SignedLastBlock: !(i == 1 && b%3 == 0) && !(i == 4 && b >= 3), // val 4 goes offline and is jailed
			}
		}
		var tr celestiaDiffTrace
		tr.begin = app.BeginBlock(abci.RequestBeginBlock{Header: header, LastCommitInfo: abci.LastCommitInfo{Votes: votes}})

		dctx := app.BaseApp.NewContext(false, header)
		switch b {
		case 8: // param changes, as a passed gov proposal would write them
			sp := app.StakingKeeper.GetParams(dctx)
			sp.HistoricalEntries = 5
			app.StakingKeeper.SetParams(dctx, sp)
			dp := app.DistrKeeper.GetParams(dctx)
			dp.CommunityTax = sdk.NewDecWithPrec(7, 2)
			app.DistrKeeper.SetParams(dctx, dp)
		case 12: // a param change in a discarded branch (e.g. a failed proposal) stays invisible
			cctx, _ := dctx.CacheContext()
			slp := app.SlashingKeeper.GetParams(cctx)
			slp.MinSignedPerWindow = sdk.NewDecWithPrec(9, 1)
			app.SlashingKeeper.SetParams(cctx, slp)
			require.Equal(t, sdk.NewDecWithPrec(5, 1), app.SlashingKeeper.GetParams(dctx).MinSignedPerWindow)
		}

		type txSpec struct {
			msg sdk.Msg
			gas uint64
		}
		specs := []txSpec{
			{stakingtypes.NewMsgDelegate(addr, valAddr(b), bond(int64(1+b%4))), 400_000},
			{stakingtypes.NewMsgDelegate(addr, sdk.ValAddress([]byte("no-such-validator-x")), bond(1)), 400_000}, // fails in msg
			{banktypes.NewMsgSend(addr, sdk.AccAddress(valSet.Validators[0].Address), sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 10))), 400_000},
		}
		if b%2 == 1 {
			specs = append(specs,
				txSpec{stakingtypes.NewMsgUndelegate(addr, valAddr(b-1), bond(1)), 400_000},
				txSpec{stakingtypes.NewMsgBeginRedelegate(addr, valAddr(b+2), valAddr(b+3), bond(1)), 400_000},
				txSpec{stakingtypes.NewMsgUndelegate(addr, valAddr(b), bond(1_000_000)), 400_000}, // more than delegated
			)
		}
		if fullGas > 0 {
			// out-of-gas sweep: limits from far below the ante cost up to a full delegation
			for k := 0; k < 3; k++ {
				g := 20_000 + (fullGas-20_000)*uint64(b*3+k)/uint64(nBlocks*3) + uint64(rnd.Intn(97))
				specs = append(specs, txSpec{stakingtypes.NewMsgDelegate(addr, valAddr(b+k), bond(1)), g})
			}
		}

		for _, s := range specs {
			seq := app.AccountKeeper.GetAccount(app.BaseApp.NewContext(false, header), addr).GetSequence()
			tx, err := helpers.GenSignedMockTx(rnd, enc.TxConfig, []sdk.Msg{s.msg}, sdk.Coins{sdk.NewInt64Coin(sdk.DefaultBondDenom, 1)}, s.gas, "", []uint64{0}, []uint64{seq}, priv)
			require.NoError(t, err)
			bz, err := enc.TxConfig.TxEncoder()(tx)
			require.NoError(t, err)
			tr.check = append(tr.check, app.CheckTx(abci.RequestCheckTx{Tx: bz}))
			r := app.DeliverTx(abci.RequestDeliverTx{Tx: bz})
			tr.deliver = append(tr.deliver, r)
			if r.Code == sdkerrors.ErrOutOfGas.ABCICode() {
				if app.AccountKeeper.GetAccount(app.BaseApp.NewContext(false, header), addr).GetSequence() > seq {
					oogInMsg++
				} else {
					oogInAnte++
				}
			}
			if b == 0 && fullGas == 0 && r.Code == 0 {
				fullGas = uint64(r.GasUsed)
			}
		}
		tr.end = app.EndBlock(abci.RequestEndBlock{Height: h})
		tr.query = <-queryDone + qparams
		tr.appHash = app.Commit().Data
		out = append(out, tr)
	}
	ctx = app.BaseApp.NewContext(true, tmproto.Header{})
	v, found := app.StakingKeeper.GetValidator(ctx, valAddr(4))
	require.True(t, found)
	require.True(t, v.Jailed, "validator 4 should be jailed for downtime")
	return out, oogInAnte, oogInMsg
}

// TestCelestiaExecCachesDifferential compares the full node-visible output of
// a chain with [performance] execution-caches off and on.
func TestCelestiaExecCachesDifferential(t *testing.T) {
	const n = 40
	off, anteOff, msgOff := runCelestiaDiffChain(t, false, n)
	on, anteOn, msgOn := runCelestiaDiffChain(t, true, n)
	require.Positive(t, anteOff, "no tx ran out of gas in the ante handler")
	require.Positive(t, msgOff, "no tx ran out of gas during message execution")
	require.Equal(t, anteOff, anteOn)
	require.Equal(t, msgOff, msgOn)
	require.Len(t, on, n)
	for b := range off {
		require.Equal(t, off[b].begin, on[b].begin, "BeginBlock differs at block %d", b)
		require.Equal(t, off[b].check, on[b].check, "CheckTx differs at block %d", b)
		require.Equal(t, off[b].deliver, on[b].deliver, "DeliverTx differs at block %d", b)
		require.Equal(t, off[b].end, on[b].end, "EndBlock differs at block %d", b)
		require.Equal(t, off[b].query, on[b].query, "query differs at block %d", b)
		require.Equal(t, off[b].appHash, on[b].appHash, "app hash differs at block %d", b)
	}
}
