package server_test

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	cmtstate "github.com/cometbft/cometbft/proto/tendermint/state"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	sm "github.com/cometbft/cometbft/state"
	cmtstore "github.com/cometbft/cometbft/store"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cometbft/cometbft/version"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	pruningtypes "cosmossdk.io/store/pruning/types"
	"cosmossdk.io/store/rootmulti"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/cosmos/cosmos-sdk/server/api"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

const rbChainID = "rollback-deferred"

var rbKey = storetypes.NewKVStoreKey("a")

type rbApp struct{ *baseapp.BaseApp }

func (rbApp) RegisterAPIRoutes(*api.Server, serverconfig.APIConfig)   {}
func (rbApp) RegisterTxService(client.Context)                        {}
func (rbApp) RegisterTendermintService(client.Context)                {}
func (rbApp) RegisterNodeService(client.Context, serverconfig.Config) {}

// newRBApp writes the block height into its store in every block.
func newRBApp(logger log.Logger, db dbm.DB, _ io.Writer, appOpts servertypes.AppOptions) servertypes.Application {
	app := baseapp.NewBaseApp("rollback", logger, db, nil, server.DefaultBaseappOptions(appOpts)...)
	app.MountStores(rbKey)
	app.SetBeginBlocker(func(ctx sdk.Context) (sdk.BeginBlock, error) {
		ctx.KVStore(rbKey).Set([]byte("height"), sdk.Uint64ToBigEndian(uint64(ctx.BlockHeight())))
		return sdk.BeginBlock{}, nil
	})
	if err := app.LoadLatestVersion(); err != nil {
		panic(err)
	}
	return rbApp{app}
}

type rbFixture struct {
	cfg     *cmtcfg.Config
	valSet  *cmttypes.ValidatorSet
	st      sm.State
	appHash map[int64][]byte
}

func (f *rbFixture) viper() *viper.Viper {
	v := viper.New()
	v.Set(flags.FlagHome, f.cfg.RootDir)
	v.Set(flags.FlagChainID, rbChainID)
	v.Set(server.FlagPruning, pruningtypes.PruningOptionDefault)
	v.Set(server.FlagDeferredPersistence, rootmulti.DeferredSync)
	return v
}

func (f *rbFixture) appDB(t *testing.T) dbm.DB {
	t.Helper()
	db, err := dbm.NewDB("application", server.GetAppDBBackend(f.viper()), filepath.Join(f.cfg.RootDir, "data"))
	require.NoError(t, err)
	return db
}

func (f *rbFixture) coreStores(t *testing.T) (*cmtstore.BlockStore, sm.Store) {
	t.Helper()
	blockDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "blockstore", Config: f.cfg})
	require.NoError(t, err)
	stateDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "state", Config: f.cfg})
	require.NoError(t, err)
	return cmtstore.NewBlockStore(blockDB), sm.NewStore(stateDB, sm.StoreOptions{})
}

// saveCore writes CometBFT blocks and states for heights from..to.
func (f *rbFixture) saveCore(t *testing.T, from, to int64) {
	t.Helper()
	bs, ss := f.coreStores(t)
	defer func() {
		require.NoError(t, bs.Close())
		require.NoError(t, ss.Close())
	}()
	if from == 1 {
		params := cmttypes.DefaultConsensusParams()
		f.st = sm.State{
			Version:                          cmtstate.Version{Consensus: cmtversion.Consensus{Block: version.BlockProtocol, App: 0}},
			ChainID:                          rbChainID,
			InitialHeight:                    1,
			LastValidators:                   f.valSet,
			Validators:                       f.valSet,
			NextValidators:                   f.valSet,
			LastHeightValidatorsChanged:      1,
			ConsensusParams:                  *params,
			LastHeightConsensusParamsChanged: 1,
		}
		require.NoError(t, ss.Save(f.st))
	}
	for h := from; h <= to; h++ {
		block := &cmttypes.Block{
			Header: cmttypes.Header{
				Version:         cmtversion.Consensus{Block: version.BlockProtocol},
				ChainID:         rbChainID,
				Height:          h,
				AppHash:         f.appHash[h-1],
				ValidatorsHash:  f.valSet.Hash(),
				ProposerAddress: f.valSet.GetProposer().Address,
			},
			LastCommit: &cmttypes.Commit{Height: h - 1},
		}
		parts, err := block.MakePartSet(cmttypes.BlockPartSizeBytes)
		require.NoError(t, err)
		bs.SaveBlock(block, parts, &cmttypes.Commit{Height: h})
		f.st.LastBlockHeight = h
		f.st.LastBlockID = cmttypes.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
		f.st.AppHash = f.appHash[h]
		require.NoError(t, ss.Save(f.st))
	}
}

// execute runs heights from..to on app; heights up to behindUntil are an hour old.
func (f *rbFixture) execute(t *testing.T, app servertypes.Application, from, to, behindUntil int64) {
	t.Helper()
	for h := from; h <= to; h++ {
		ts := time.Now()
		if h <= behindUntil {
			ts = ts.Add(-time.Hour)
		}
		res, err := app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: h, Time: ts})
		require.NoError(t, err)
		_, err = app.Commit()
		require.NoError(t, err)
		f.appHash[h] = res.AppHash
	}
}

func (f *rbFixture) run(t *testing.T) error {
	t.Helper()
	cmd := server.NewRollbackCmd(newRBApp, f.cfg.RootDir)
	cmd.SetArgs([]string{})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	sctx := server.NewContext(f.viper(), f.cfg, log.NewNopLogger())
	return cmd.ExecuteContext(context.WithValue(context.Background(), server.ServerContextKey, sctx))
}

func (f *rbFixture) requireHeights(t *testing.T, blockHeight, stateHeight, appHeight int64) {
	t.Helper()
	bs, ss := f.coreStores(t)
	st, err := ss.Load()
	require.NoError(t, err)
	require.Equal(t, blockHeight, bs.Height())
	require.Equal(t, stateHeight, st.LastBlockHeight)
	require.NoError(t, bs.Close())
	require.NoError(t, ss.Close())

	db := f.appDB(t)
	_, deferred, err := rootmulti.ReadDeferredMarker(db)
	require.NoError(t, err)
	require.False(t, deferred)
	cid := rootmulti.GetLatestVersion(db)
	require.NoError(t, db.Close())
	require.Equal(t, appHeight, cid)
}

// TestRollbackCmdAfterSyncLeaves: right after "sync" leaves deferred mode at S
// the app holds only version S, so a rollback to S-1 is refused before
// CometBFT state is touched. One commit later, a rollback to S works.
func TestRollbackCmdAfterSyncLeaves(t *testing.T) {
	cfg := cmtcfg.DefaultConfig()
	cfg.SetRoot(t.TempDir())
	valSet, _ := cmttypes.RandValidatorSet(1, 10)
	f := &rbFixture{cfg: cfg, valSet: valSet, appHash: map[int64][]byte{}}

	// Heights 1..10 are behind ("sync" is on); 11 is at the tip, so it leaves at S=11.
	const s = int64(11)
	app := newRBApp(log.NewNopLogger(), f.appDB(t), nil, f.viper())
	_, err := app.InitChain(&abci.RequestInitChain{ChainId: rbChainID, InitialHeight: 1})
	require.NoError(t, err)
	f.execute(t, app, 1, s, s-1)
	require.NoError(t, app.Close())
	f.saveCore(t, 1, s)

	err = f.run(t)
	require.ErrorContains(t, err, "CometBFT state is untouched")
	require.ErrorContains(t, err, "cannot roll back to height 10")
	f.requireHeights(t, s, s, s)

	// One more commit at the tip keeps S on disk.
	app = newRBApp(log.NewNopLogger(), f.appDB(t), nil, f.viper())
	f.execute(t, app, s+1, s+1, 0)
	require.NoError(t, app.Close())
	f.saveCore(t, s+1, s+1)

	require.NoError(t, f.run(t))
	f.requireHeights(t, s+1, s, s)
}
