package simapp

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"

	sdkmath "cosmossdk.io/math"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// Differential test for the [performance] execution-caches and
// blocksync-events-off options. The same block sequence runs in three child
// processes (so no cache state is shared): caches off, caches on, and caches
// on with events-off. Every FinalizeBlock and CheckTx response must be
// identical, except that with events-off only event index flags and the
// distribution amount attributes may differ.

const (
	celestiaDiffOutEnv  = "CELESTIA_EXEC_DIFF_OUT"
	celestiaDiffModeEnv = "CELESTIA_EXEC_DIFF_MODE" // off, on or events-off
)

type celestiaDiffRun struct {
	Flags                  [2]bool                       // execution caches, events-off
	Blocks                 []*abci.ResponseFinalizeBlock `json:"-"`
	CheckTxs               []*abci.ResponseCheckTx       `json:"-"`
	RawBlocks, RawCheckTxs [][]byte                      // protobuf encodings (abci oneofs do not JSON-decode)
}

func TestCelestiaExecCachesDifferential(t *testing.T) {
	if os.Getenv(celestiaDiffOutEnv) != "" {
		t.Skip("child process")
	}
	if testing.Short() {
		t.Skip("spawns child processes")
	}
	off := celestiaRunDiffChild(t, "off")
	on := celestiaRunDiffChild(t, "on")
	all := celestiaRunDiffChild(t, "events-off")

	require.Equal(t, [2]bool{false, false}, off.Flags)
	require.Equal(t, [2]bool{true, false}, on.Flags)
	require.Equal(t, [2]bool{true, true}, all.Flags)

	// sanity: the workload hits out-of-gas, failing and passing txs, param updates and jailing
	codes := map[uint32]int{}
	var oog, jailed, passed int
	for _, b := range off.Blocks {
		for _, r := range b.TxResults {
			codes[r.Code]++
			if r.Codespace == "sdk" && r.Code == 11 {
				oog++
			}
		}
		for _, e := range b.Events {
			if e.Type == slashingtypes.EventTypeSlash {
				jailed++
			}
			for _, a := range e.Attributes {
				if a.Value == govtypes.AttributeValueProposalPassed {
					passed++
				}
			}
		}
	}
	require.Greater(t, codes[0], 50, "too few successful txs")
	require.Greater(t, oog, 10, "too few out-of-gas txs")
	require.Greater(t, len(codes), 2, "too few distinct failure codes")
	require.Greater(t, jailed, 0, "no validator was slashed for downtime")
	require.Equal(t, 2, passed, "the staking and slashing MsgUpdateParams proposals did not both pass")

	require.Equal(t, len(off.Blocks), len(on.Blocks))
	for i := range off.Blocks {
		require.Equal(t, off.Blocks[i], on.Blocks[i], "block %d differs (caches on)", i)
	}
	require.Equal(t, off.CheckTxs, on.CheckTxs, "CheckTx responses differ (caches on)")

	celestiaNormalizeEvents(off)
	celestiaNormalizeEvents(all)
	for i := range off.Blocks {
		require.Equal(t, off.Blocks[i], all.Blocks[i], "block %d differs (all on, modulo events-off)", i)
	}
	require.Equal(t, off.CheckTxs, all.CheckTxs, "CheckTx responses differ (all on)")
}

func celestiaRunDiffChild(t *testing.T, mode string) *celestiaDiffRun {
	t.Helper()
	out := filepath.Join(t.TempDir(), "run.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCelestiaExecCachesDifferentialChild$", "-test.count=1") //nolint:gosec // re-runs this test binary
	cmd.Env = append(os.Environ(), celestiaDiffOutEnv+"="+out, celestiaDiffModeEnv+"="+mode)
	bz, err := cmd.CombinedOutput()
	require.NoError(t, err, "child %s failed:\n%s", mode, bz)
	raw, err := os.ReadFile(out) //nolint:gosec // path is the temp file this test created
	require.NoError(t, err)
	var run celestiaDiffRun
	require.NoError(t, json.Unmarshal(raw, &run))
	for _, bz := range run.RawBlocks {
		var res abci.ResponseFinalizeBlock
		require.NoError(t, res.Unmarshal(bz))
		run.Blocks = append(run.Blocks, &res)
	}
	for _, bz := range run.RawCheckTxs {
		var res abci.ResponseCheckTx
		require.NoError(t, res.Unmarshal(bz))
		run.CheckTxs = append(run.CheckTxs, &res)
	}
	return &run
}

// celestiaNormalizeEvents drops what blocksync-events-off is allowed to change.
func celestiaNormalizeEvents(run *celestiaDiffRun) {
	norm := func(evs []abci.Event) {
		for i := range evs {
			for j := range evs[i].Attributes {
				a := &evs[i].Attributes[j]
				a.Index = false
				if (evs[i].Type == "commission" || evs[i].Type == "rewards") && a.Key == sdk.AttributeKeyAmount {
					a.Value = ""
				}
			}
		}
	}
	for _, b := range run.Blocks {
		norm(b.Events)
		for _, r := range b.TxResults {
			norm(r.Events)
		}
	}
}

// TestCelestiaExecCachesDifferentialChild runs the workload; it only runs as a
// child of TestCelestiaExecCachesDifferential.
func TestCelestiaExecCachesDifferentialChild(t *testing.T) {
	outPath := os.Getenv(celestiaDiffOutEnv)
	if outPath == "" {
		t.Skip("only runs as a child of TestCelestiaExecCachesDifferential")
	}
	mode := os.Getenv(celestiaDiffModeEnv)
	sdk.SetExecutionCaches(mode != "off")
	sdk.SetBlocksyncEventsOff(mode == "events-off")

	const (
		numVals   = 5
		numAccs   = 30
		numBlocks = 30
	)
	denom := sdk.DefaultBondDenom
	genesisTime := time.Unix(1_700_000_000, 0).UTC()

	// validators (deterministic keys)
	var cmtVals []*cmttypes.Validator
	for i := 0; i < numVals; i++ {
		pk := ed25519.GenPrivKeyFromSecret([]byte(fmt.Sprintf("val-%d", i))).PubKey()
		cpk, err := cryptocodec.ToCmtPubKeyInterface(pk)
		require.NoError(t, err)
		cmtVals = append(cmtVals, cmttypes.NewValidator(cpk, 1))
	}
	valSet := cmttypes.NewValidatorSet(cmtVals)
	valAddrs := make([]string, numVals) // valoper bech32, in valSet order
	consAddrs := make([][]byte, numVals)
	for i, v := range valSet.Validators {
		valAddrs[i] = sdk.ValAddress(v.Address).String()
		consAddrs[i] = v.Address
	}

	// accounts (deterministic keys)
	privs := make([]cryptotypes.PrivKey, numAccs)
	addrs := make([]sdk.AccAddress, numAccs)
	var genAccs []authtypes.GenesisAccount
	var balances []banktypes.Balance
	for i := range privs {
		privs[i] = secp256k1.GenPrivKeyFromSecret([]byte(fmt.Sprintf("acc-%d", i)))
		addrs[i] = sdk.AccAddress(privs[i].PubKey().Address())
		genAccs = append(genAccs, authtypes.NewBaseAccount(addrs[i], privs[i].PubKey(), uint64(i), 0))
		balances = append(balances, banktypes.Balance{Address: addrs[i].String(), Coins: sdk.NewCoins(sdk.NewCoin(denom, sdkmath.NewInt(1_000_000_000_000)))})
	}

	app, genesis := setup(true, 0) // crisis invariants off: the genesis built here skips distribution records
	cdc := app.AppCodec()
	genesis, err := simtestutil.GenesisStateWithValSet(cdc, genesis, valSet, genAccs, balances...)
	require.NoError(t, err)

	// GenesisStateWithValSet funds the bonded pool for a single validator only
	var bankGen banktypes.GenesisState
	cdc.MustUnmarshalJSON(genesis[banktypes.ModuleName], &bankGen)
	bonded := authtypes.NewModuleAddress(stakingtypes.BondedPoolName).String()
	for i := range bankGen.Balances {
		if bankGen.Balances[i].Address == bonded {
			bankGen.Balances[i].Coins = sdk.NewCoins(sdk.NewCoin(denom, sdk.DefaultPowerReduction.MulRaw(numVals)))
		}
	}
	genesis[banktypes.ModuleName] = cdc.MustMarshalJSON(&bankGen)

	// short gov periods, a small slashing window and few historical entries
	var govGen govv1.GenesisState
	cdc.MustUnmarshalJSON(genesis[govtypes.ModuleName], &govGen)
	voting, expedited := 4*time.Second, 2*time.Second
	govGen.Params.VotingPeriod, govGen.Params.ExpeditedVotingPeriod = &voting, &expedited
	genesis[govtypes.ModuleName] = cdc.MustMarshalJSON(&govGen)
	var slashGen slashingtypes.GenesisState
	cdc.MustUnmarshalJSON(genesis[slashingtypes.ModuleName], &slashGen)
	slashGen.Params.SignedBlocksWindow = 6
	slashGen.Params.MinSignedPerWindow = sdkmath.LegacyNewDecWithPrec(5, 1)
	for _, ca := range consAddrs {
		addr := sdk.ConsAddress(ca)
		slashGen.SigningInfos = append(slashGen.SigningInfos, slashingtypes.SigningInfo{
			Address:              addr.String(),
			ValidatorSigningInfo: slashingtypes.NewValidatorSigningInfo(addr, 0, 0, time.Unix(0, 0).UTC(), false, 0),
		})
	}
	genesis[slashingtypes.ModuleName] = cdc.MustMarshalJSON(&slashGen)
	var stakingGen stakingtypes.GenesisState
	cdc.MustUnmarshalJSON(genesis[stakingtypes.ModuleName], &stakingGen)
	stakingGen.Params.HistoricalEntries = 4
	genesis[stakingtypes.ModuleName] = cdc.MustMarshalJSON(&stakingGen)

	stateBytes, err := json.Marshal(genesis)
	require.NoError(t, err)
	_, err = app.InitChain(&abci.RequestInitChain{
		Time:            genesisTime,
		ConsensusParams: simtestutil.DefaultConsensusParams,
		AppStateBytes:   stateBytes,
	})
	require.NoError(t, err)

	run := &celestiaDiffRun{Flags: [2]bool{sdk.ExecutionCachesEnabled(), sdk.BlocksyncEventsOff()}}

	// Concurrent readers, as on a validator at the tip: gRPC-style queries on
	// the last committed state. (Concurrent CheckTx is left out: it trips a
	// data race on a captured err in cosmossdk.io/x/tx v0.13.7 GetSigners,
	// with the caches off as well.) Queries are kept out of Commit, which
	// races with IAVL's fast-storage version read, also with the caches off.
	var (
		stop     = make(chan struct{})
		wg       sync.WaitGroup
		commitMu sync.RWMutex
	)
	querier := stakingkeeper.Querier{Keeper: app.StakingKeeper}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			commitMu.RLock()
			if app.LastBlockHeight() < 1 {
				commitMu.RUnlock()
				time.Sleep(time.Millisecond)
				continue
			}
			qctx, err := app.CreateQueryContext(0, false)
			if err != nil {
				commitMu.RUnlock()
				continue
			}
			_, _ = querier.Validators(qctx, &stakingtypes.QueryValidatorsRequest{})
			_, _ = querier.Validator(qctx, &stakingtypes.QueryValidatorRequest{ValidatorAddr: valAddrs[i%numVals]})
			_, _ = app.StakingKeeper.GetParams(qctx)
			_, _ = app.SlashingKeeper.GetParams(qctx)
			_, _ = app.StakingKeeper.ValidatorByConsAddr(qctx, consAddrs[i%numVals])
			_, _ = sdk.AccAddressFromBech32(addrs[i%numAccs].String())
			commitMu.RUnlock()
		}
	}()
	defer func() { close(stop); wg.Wait() }()

	r := rand.New(rand.NewSource(7)) //nolint:gosec // deterministic test input
	txCfg := app.TxConfig()
	fee := sdk.NewCoins(sdk.NewCoin(denom, sdkmath.NewInt(2000)))
	govAuthority := authtypes.NewModuleAddress(govtypes.ModuleName).String()

	signTx := func(i int, gas uint64, msgs ...sdk.Msg) []byte {
		qctx, err := app.CreateQueryContext(0, false)
		require.NoError(t, err)
		acc := app.AccountKeeper.GetAccount(qctx, addrs[i])
		require.NotNil(t, acc)
		tx, err := simtestutil.GenSignedMockTx(r, txCfg, msgs, fee, gas, "", []uint64{acc.GetAccountNumber()}, []uint64{acc.GetSequence()}, privs[i])
		require.NoError(t, err)
		bz, err := txCfg.TxEncoder()(tx)
		require.NoError(t, err)
		return bz
	}

	stakingParams := stakingtypes.DefaultParams()
	stakingParams.HistoricalEntries = 2
	stakingParams.MaxValidators = 4
	stakingParams.MinCommissionRate = sdkmath.LegacyZeroDec()
	slashingParams := slashGen.Params
	slashingParams.SignedBlocksWindow = 4
	slashingParams.MinSignedPerWindow = sdkmath.LegacyNewDecWithPrec(75, 2)

	for h := int64(1); h <= numBlocks; h++ {
		var txs [][]byte
		if h > 1 {
			for i := 0; i < numAccs; i++ {
				gas := uint64(400_000)
				if r.Intn(10) < 4 {
					gas = uint64(30_000 + r.Intn(150_000)) // out of gas somewhere in ante or msg execution
				}
				val := valAddrs[r.Intn(numVals)]
				amt := sdk.NewCoin(denom, sdkmath.NewInt(int64(1+r.Intn(3_000_000))))
				var msg sdk.Msg
				switch {
				case i == 0 && h == 2:
					m1, err := govv1.NewMsgSubmitProposal([]sdk.Msg{&stakingtypes.MsgUpdateParams{Authority: govAuthority, Params: stakingParams}},
						sdk.NewCoins(sdk.NewCoin(denom, sdkmath.NewInt(10_000_000))), addrs[0].String(), "", "staking", "staking", false)
					require.NoError(t, err)
					// a large stake so that account 0's yes votes meet quorum
					big := stakingtypes.NewMsgDelegate(addrs[0].String(), valAddrs[0], sdk.NewCoin(denom, sdkmath.NewInt(100_000_000_000)))
					txs = append(txs, signTx(0, 1_000_000, m1, big))
					continue
				case i == 0 && h == 3:
					m2, err := govv1.NewMsgSubmitProposal([]sdk.Msg{&slashingtypes.MsgUpdateParams{Authority: govAuthority, Params: slashingParams}},
						sdk.NewCoins(sdk.NewCoin(denom, sdkmath.NewInt(10_000_000))), addrs[0].String(), "", "slashing", "slashing", false)
					require.NoError(t, err)
					txs = append(txs, signTx(0, 1_000_000, m2, govv1.NewMsgVote(addrs[0], 1, govv1.OptionYes, "")))
					continue
				case i == 0 && h == 4:
					msg = govv1.NewMsgVote(addrs[0], 2, govv1.OptionYes, "")
				case i == 0:
					continue // keep the gov voter's stake fixed
				default:
					switch r.Intn(6) {
					case 0:
						msg = banktypes.NewMsgSend(addrs[i], addrs[r.Intn(numAccs)], sdk.NewCoins(amt))
					case 1, 2:
						msg = stakingtypes.NewMsgDelegate(addrs[i].String(), val, amt)
					case 3:
						msg = stakingtypes.NewMsgUndelegate(addrs[i].String(), val, sdk.NewCoin(denom, amt.Amount.QuoRaw(4)))
					case 4:
						// invalid validator address: wrong prefix, then a corrupted checksum
						bad := sdk.AccAddress(consAddrs[r.Intn(numVals)]).String()
						if r.Intn(2) == 0 {
							bad = val[:len(val)-1] + "q"
						}
						msg = stakingtypes.NewMsgDelegate(addrs[i].String(), bad, amt)
					default:
						msg = stakingtypes.NewMsgBeginRedelegate(addrs[i].String(), val, valAddrs[r.Intn(numVals)], sdk.NewCoin(denom, amt.Amount.QuoRaw(8)))
					}
				}
				txs = append(txs, signTx(i, gas, msg))
			}
		}

		for _, tx := range txs {
			res, err := app.CheckTx(&abci.RequestCheckTx{Tx: tx, Type: abci.CheckTxType_New})
			require.NoError(t, err)
			run.CheckTxs = append(run.CheckTxs, res)
		}

		// validator 4 never signs, validator 3 skips every other block
		var votes []abci.VoteInfo
		for i, v := range valSet.Validators {
			flag := cmtproto.BlockIDFlagCommit
			if i == 4 || (i == 3 && h%2 == 1) {
				flag = cmtproto.BlockIDFlagAbsent
			}
			votes = append(votes, abci.VoteInfo{Validator: abci.Validator{Address: v.Address, Power: v.VotingPower}, BlockIdFlag: flag})
		}
		res, err := app.FinalizeBlock(&abci.RequestFinalizeBlock{
			Height:            h,
			Time:              genesisTime.Add(time.Duration(h) * time.Second),
			Txs:               txs,
			DecidedLastCommit: abci.CommitInfo{Votes: votes},
			ProposerAddress:   valSet.Validators[int(h)%numVals].Address,
		})
		require.NoError(t, err)
		commitMu.Lock()
		_, err = app.Commit()
		commitMu.Unlock()
		require.NoError(t, err)
		run.Blocks = append(run.Blocks, res)
	}

	for _, res := range run.Blocks {
		bz, err := res.Marshal()
		require.NoError(t, err)
		run.RawBlocks = append(run.RawBlocks, bz)
	}
	for _, res := range run.CheckTxs {
		bz, err := res.Marshal()
		require.NoError(t, err)
		run.RawCheckTxs = append(run.RawCheckTxs, bz)
	}
	bz, err := json.Marshal(run)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(outPath, bz, 0o600))
}
