package server_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtcmd "github.com/cometbft/cometbft/cmd/cometbft/commands"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtstate "github.com/cometbft/cometbft/proto/tendermint/state"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	sm "github.com/cometbft/cometbft/state"
	cmtstore "github.com/cometbft/cometbft/store"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cometbft/cometbft/version"
	dbm "github.com/cosmos/cosmos-db"
	gogotypes "github.com/cosmos/gogoproto/types"
	"github.com/spf13/cast"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/iavl"
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

const (
	rollbackChainID = "rollback-chain"
	// rollbackUpgradeHeightKey is the test app option for the first height on app version 2.
	rollbackUpgradeHeightKey = "rollback-test-upgrade-height"
)

var (
	rollbackKeyA = storetypes.NewKVStoreKey("a")
	rollbackKeyB = storetypes.NewKVStoreKey("b")
)

type rollbackTestApp struct{ *baseapp.BaseApp }

func (rollbackTestApp) RegisterAPIRoutes(*api.Server, serverconfig.APIConfig)   {}
func (rollbackTestApp) RegisterTxService(client.Context)                        {}
func (rollbackTestApp) RegisterTendermintService(client.Context)                {}
func (rollbackTestApp) RegisterNodeService(client.Context, serverconfig.Config) {}

// rollbackVersionModifier keeps the app version in store a.
type rollbackVersionModifier struct{}

func (rollbackVersionModifier) SetAppVersion(ctx context.Context, v uint64) error {
	sdk.UnwrapSDKContext(ctx).KVStore(rollbackKeyA).Set([]byte("version"), sdk.Uint64ToBigEndian(v))
	return nil
}

func (rollbackVersionModifier) AppVersion(ctx context.Context) (uint64, error) {
	bz := sdk.UnwrapSDKContext(ctx).KVStore(rollbackKeyA).Get([]byte("version"))
	if bz == nil {
		return 0, errors.New("app version not set")
	}
	return sdk.BigEndianToUint64(bz), nil
}

// newRollbackTestApp writes the block height and app version into its stores in every block.
func newRollbackTestApp(logger log.Logger, db dbm.DB, _ io.Writer, appOpts servertypes.AppOptions) servertypes.Application {
	upgradeHeight := cast.ToInt64(appOpts.Get(rollbackUpgradeHeightKey))
	app := baseapp.NewBaseApp("rollback", logger, db, nil, server.DefaultBaseappOptions(appOpts)...)
	app.MountStores(rollbackKeyA, rollbackKeyB)
	app.SetVersionModifier(rollbackVersionModifier{})
	app.SetBeginBlocker(func(ctx sdk.Context) (sdk.BeginBlock, error) {
		height := sdk.Uint64ToBigEndian(uint64(ctx.BlockHeight()))
		ctx.KVStore(rollbackKeyA).Set([]byte("height"), height)
		ctx.KVStore(rollbackKeyB).Set([]byte("height"), height)
		appVersion := uint64(1)
		if upgradeHeight > 0 && ctx.BlockHeight() >= upgradeHeight {
			appVersion = 2
		}
		return sdk.BeginBlock{}, rollbackVersionModifier{}.SetAppVersion(ctx, appVersion)
	})
	if err := app.LoadLatestVersion(); err != nil {
		panic(err)
	}
	return rollbackTestApp{app}
}

func newRollbackViper(home string) *viper.Viper {
	v := viper.New()
	v.Set(flags.FlagHome, home)
	v.Set(flags.FlagChainID, rollbackChainID)
	v.Set(server.FlagPruning, pruningtypes.PruningOptionNothing)
	return v
}

// runRollbackChain executes heights 1..n and returns the app hash after each height.
func runRollbackChain(t *testing.T, app servertypes.Application, n int64) map[int64][]byte {
	t.Helper()
	_, err := app.InitChain(&abci.RequestInitChain{ChainId: rollbackChainID, InitialHeight: 1})
	require.NoError(t, err)
	appHash := make(map[int64][]byte)
	for h := int64(1); h <= n; h++ {
		res, err := app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: h})
		require.NoError(t, err)
		_, err = app.Commit()
		require.NoError(t, err)
		appHash[h] = res.AppHash
	}
	return appHash
}

// writeRollbackMarker writes rootmulti's marker for an unfinished rollback to target.
func writeRollbackMarker(t *testing.T, db dbm.DB, target int64) {
	t.Helper()
	bz, err := gogotypes.StdInt64Marshal(target)
	require.NoError(t, err)
	require.NoError(t, db.SetSync([]byte("s/rollback"), bz))
}

// requirePendingRollbackPanic requires a normal app open on db to refuse a pending rollback to target.
func requirePendingRollbackPanic(t *testing.T, db dbm.DB, v *viper.Viper, target int64) {
	t.Helper()
	defer func() {
		err, ok := recover().(error)
		require.True(t, ok)
		require.ErrorIs(t, err, rootmulti.ErrRollbackInProgress{Target: target})
	}()
	newRollbackTestApp(log.NewNopLogger(), db, nil, v)
}

type fixtureOpts struct {
	appVersionChangeAt   int64 // first state height with app version 2
	pruneKeepRecent      uint64
	extraUnexecutedBlock bool
	badHeaderAppHashAt   int64
}

type rollbackFixture struct {
	cfg     *cmtcfg.Config
	opts    fixtureOpts
	appHash map[int64][]byte
}

// newRollbackFixture runs the app for n heights and writes matching CometBFT blocks and states.
func newRollbackFixture(t *testing.T, n int64, opts fixtureOpts) *rollbackFixture {
	t.Helper()
	cfg := cmtcfg.DefaultConfig()
	cfg.SetRoot(t.TempDir())
	f := &rollbackFixture{cfg: cfg, opts: opts}

	app := newRollbackTestApp(log.NewNopLogger(), f.appDB(t), nil, f.viper())
	f.appHash = runRollbackChain(t, app, n)
	require.NoError(t, app.Close())

	bs, ss := f.openCoreStores(t)
	defer closeCoreStores(t, bs, ss)

	valSet, _ := cmttypes.RandValidatorSet(1, 10)
	params := cmttypes.DefaultConsensusParams()
	params.Version.App = 1
	st := sm.State{
		Version:                          cmtstate.Version{Consensus: cmtversion.Consensus{Block: version.BlockProtocol, App: 1}},
		ChainID:                          rollbackChainID,
		InitialHeight:                    1,
		LastValidators:                   valSet,
		Validators:                       valSet,
		NextValidators:                   valSet,
		LastHeightValidatorsChanged:      1,
		ConsensusParams:                  *params,
		LastHeightConsensusParamsChanged: 1,
	}
	require.NoError(t, ss.Save(st))

	blocks := n
	if opts.extraUnexecutedBlock {
		blocks++
	}
	for h := int64(1); h <= blocks; h++ {
		headerAppHash := f.appHash[h-1]
		if h == opts.badHeaderAppHashAt {
			headerAppHash = tmhash.Sum([]byte("bad"))
		}
		block := &cmttypes.Block{
			Header: cmttypes.Header{
				Version:         cmtversion.Consensus{Block: version.BlockProtocol},
				ChainID:         rollbackChainID,
				Height:          h,
				AppHash:         headerAppHash,
				ValidatorsHash:  valSet.Hash(),
				ProposerAddress: valSet.GetProposer().Address,
			},
			LastCommit: &cmttypes.Commit{Height: h - 1},
		}
		parts, err := block.MakePartSet(cmttypes.BlockPartSizeBytes)
		require.NoError(t, err)
		bs.SaveBlock(block, parts, &cmttypes.Commit{Height: h})
		if h > n {
			break
		}

		if h == opts.appVersionChangeAt {
			st.Version.Consensus.App = 2
			st.ConsensusParams.Version.App = 2
			st.LastHeightConsensusParamsChanged = h + 1
		}
		st.LastBlockHeight = h
		st.LastBlockID = cmttypes.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
		st.AppHash = f.appHash[h]
		require.NoError(t, ss.Save(st))
	}
	return f
}

func (f *rollbackFixture) viper() *viper.Viper {
	v := newRollbackViper(f.cfg.RootDir)
	v.Set(rollbackUpgradeHeightKey, f.opts.appVersionChangeAt)
	if f.opts.pruneKeepRecent > 0 {
		v.Set(server.FlagPruning, pruningtypes.PruningOptionCustom)
		v.Set(server.FlagPruningKeepRecent, f.opts.pruneKeepRecent)
		v.Set(server.FlagPruningInterval, 10)
	}
	return v
}

func (f *rollbackFixture) appDB(t *testing.T) dbm.DB {
	t.Helper()
	db, err := dbm.NewDB("application", server.GetAppDBBackend(f.viper()), filepath.Join(f.cfg.RootDir, "data"))
	require.NoError(t, err)
	return db
}

func (f *rollbackFixture) openCoreStores(t *testing.T) (*cmtstore.BlockStore, sm.Store) {
	t.Helper()
	blockDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "blockstore", Config: f.cfg, Path: f.cfg.BlockstoreDir()})
	require.NoError(t, err)
	stateDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "state", Config: f.cfg})
	require.NoError(t, err)
	return cmtstore.NewBlockStore(blockDB), sm.NewStore(stateDB, sm.StoreOptions{})
}

func closeCoreStores(t *testing.T, bs *cmtstore.BlockStore, ss sm.Store) {
	t.Helper()
	require.NoError(t, bs.Close())
	require.NoError(t, ss.Close())
}

// run executes the rollback command with args.
func (f *rollbackFixture) run(t *testing.T, args ...string) error {
	t.Helper()
	cmd := server.NewRollbackCmd(newRollbackTestApp, f.cfg.RootDir)
	cmd.SetArgs(append([]string{}, args...)) // nil args would make cobra read os.Args
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	sctx := server.NewContext(f.viper(), f.cfg, log.NewNopLogger())
	return cmd.ExecuteContext(context.WithValue(context.Background(), server.ServerContextKey, sctx))
}

func (f *rollbackFixture) requireCoreHeights(t *testing.T, blockHeight, stateHeight int64) {
	t.Helper()
	bs, ss := f.openCoreStores(t)
	defer closeCoreStores(t, bs, ss)
	st, err := ss.Load()
	require.NoError(t, err)
	require.Equal(t, blockHeight, bs.Height())
	require.Equal(t, stateHeight, st.LastBlockHeight)
}

// requireHeights checks the blockstore and state heights and the app version and hash after a normal open.
func (f *rollbackFixture) requireHeights(t *testing.T, blockHeight, stateHeight, appVersion int64) {
	t.Helper()
	f.requireCoreHeights(t, blockHeight, stateHeight)

	app := newRollbackTestApp(log.NewNopLogger(), f.appDB(t), nil, f.viper())
	defer func() { require.NoError(t, app.Close()) }()
	cid := app.CommitMultiStore().LastCommitID()
	require.Equal(t, appVersion, cid.Version)
	require.Equal(t, f.appHash[appVersion], cid.Hash)
}

func TestRollbackCmdToHeight(t *testing.T) {
	f := newRollbackFixture(t, 10, fixtureOpts{})
	require.NoError(t, f.run(t, "--height", "7"))
	f.requireHeights(t, 8, 7, 7)
}

func TestRollbackCmdDefault(t *testing.T) {
	f := newRollbackFixture(t, 10, fixtureOpts{})
	require.NoError(t, f.run(t))
	f.requireHeights(t, 10, 9, 9)
}

func TestRollbackCmdTwice(t *testing.T) {
	f := newRollbackFixture(t, 10, fixtureOpts{})
	require.NoError(t, f.run(t, "--height", "7"))
	f.requireHeights(t, 8, 7, 7)
	require.NoError(t, f.run(t, "--height", "7"))
	f.requireHeights(t, 8, 7, 7)
}

func TestRollbackCmdHard(t *testing.T) {
	f := newRollbackFixture(t, 10, fixtureOpts{})
	require.NoError(t, f.run(t, "--height", "9", "--hard"))
	f.requireHeights(t, 9, 9, 9)

	require.ErrorContains(t, f.run(t, "--height", "7", "--hard"), "--hard rolls back at most one height")
	f.requireHeights(t, 9, 9, 9)
}

func TestRollbackCmdDepthNeedsForce(t *testing.T) {
	const n = cmtcmd.MaxRollbackDepth + 3
	f := newRollbackFixture(t, n, fixtureOpts{})
	require.ErrorContains(t, f.run(t, "--height", "1"), "exceeds")
	f.requireHeights(t, n, n, n)

	require.NoError(t, f.run(t, "--height", "1", "--force"))
	f.requireHeights(t, 2, 1, 1)
}

func TestRollbackCmdRefusesAcrossUpgrade(t *testing.T) {
	f := newRollbackFixture(t, 10, fixtureOpts{appVersionChangeAt: 6})
	require.ErrorContains(t, f.run(t, "--height", "5"), "across an upgrade")
	f.requireHeights(t, 10, 10, 10)

	// Height 6 is the first state on the new version.
	require.NoError(t, f.run(t, "--height", "6"))
	f.requireHeights(t, 7, 6, 6)
}

func TestRollbackCmdAppHashMismatch(t *testing.T) {
	f := newRollbackFixture(t, 10, fixtureOpts{badHeaderAppHashAt: 8})
	require.ErrorContains(t, f.run(t, "--height", "7"), "CometBFT expects")
	f.requireHeights(t, 10, 10, 10)
}

func TestRollbackCmdAppPruned(t *testing.T) {
	f := newRollbackFixture(t, 10, fixtureOpts{pruneKeepRecent: 2})
	require.ErrorContains(t, f.run(t, "--height", "3"), "store a has no version 3")
	f.requireHeights(t, 10, 10, 10)

	// Pruning at height 10 keeps versions 8 and up.
	require.ErrorContains(t, f.run(t, "--height", "7"), "has no version 7")
	f.requireHeights(t, 10, 10, 10)
	require.NoError(t, f.run(t, "--height", "8"))
	f.requireHeights(t, 9, 8, 8)
}

func TestRollbackCmdResumeAfterCoreOnly(t *testing.T) {
	f := newRollbackFixture(t, 10, fixtureOpts{})
	_, _, err := cmtcmd.RollbackStateToHeight(f.cfg, 7, false)
	require.NoError(t, err)

	require.ErrorIs(t, f.run(t), sm.ErrBlockNotExecuted)
	require.NoError(t, f.run(t, "--height", "7"))
	f.requireHeights(t, 8, 7, 7)
}

func TestRollbackCmdResumeHalfRolledApp(t *testing.T) {
	f := newRollbackFixture(t, 10, fixtureOpts{})
	_, _, err := cmtcmd.RollbackStateToHeight(f.cfg, 7, false)
	require.NoError(t, err)

	// Leave the app DB as an app rollback to 7 killed after cutting store a.
	db := f.appDB(t)
	app := newRollbackTestApp(log.NewNopLogger(), db, nil, f.viper())
	rs := app.CommitMultiStore().(*rootmulti.Store)
	require.NoError(t, rs.GetCommitKVStore(rollbackKeyA).(*iavl.Store).LoadVersionForOverwriting(7))
	writeRollbackMarker(t, db, 7)
	require.NoError(t, app.Close())

	require.ErrorIs(t, f.run(t, "--height", "6"), rootmulti.ErrRollbackInProgress{Target: 7})
	f.requireCoreHeights(t, 8, 7)
	require.NoError(t, f.run(t, "--height", "7"))
	f.requireHeights(t, 8, 7, 7)

	app = newRollbackTestApp(log.NewNopLogger(), f.appDB(t), nil, f.viper())
	defer func() { require.NoError(t, app.Close()) }()
	rs = app.CommitMultiStore().(*rootmulti.Store)
	for _, key := range []storetypes.StoreKey{rollbackKeyA, rollbackKeyB} {
		require.False(t, rs.GetCommitKVStore(key).(*iavl.Store).VersionExists(8))
	}
}

func TestRollbackCmdAppDepthNeedsForce(t *testing.T) {
	const n = cmtcmd.MaxRollbackDepth + 3
	f := newRollbackFixture(t, n, fixtureOpts{})
	_, _, err := cmtcmd.RollbackStateToHeight(f.cfg, 1, false)
	require.NoError(t, err)

	require.ErrorContains(t, f.run(t, "--height", "1"), "app rollback depth 102 exceeds")
	f.requireHeights(t, 2, 1, n)

	require.NoError(t, f.run(t, "--height", "1", "--force"))
	f.requireHeights(t, 2, 1, 1)
}

func TestRollbackCmdAppCreatorPanics(t *testing.T) {
	f := newRollbackFixture(t, 10, fixtureOpts{})
	db := f.appDB(t)
	require.NoError(t, db.SetSync([]byte("s/rollback"), []byte{0xff})) // corrupt rootmulti marker
	require.NoError(t, db.Close())

	// The second run shows the first one released the app DB.
	for range 2 {
		var err error
		require.NotPanics(t, func() { err = f.run(t, "--height", "9") })
		require.ErrorContains(t, err, "failed to read rollback marker")
	}
}

func TestRollbackCmdRefusesAppAheadAcrossUpgrade(t *testing.T) {
	f := newRollbackFixture(t, 6, fixtureOpts{appVersionChangeAt: 6})
	_, _, err := cmtcmd.RollbackStateToHeight(f.cfg, 5, false)
	require.NoError(t, err)
	snapshotDir := filepath.Join(f.cfg.RootDir, "data", "snapshots")
	require.NoError(t, os.RemoveAll(snapshotDir))

	require.ErrorContains(t, f.run(t, "--height", "5"), "across an upgrade")
	require.NoDirExists(t, snapshotDir)
	f.requireHeights(t, 6, 5, 6)
}

func TestRollbackCmdBlockNotExecuted(t *testing.T) {
	f := newRollbackFixture(t, 10, fixtureOpts{extraUnexecutedBlock: true})
	require.ErrorContains(t, f.run(t), "block saved but not executed")
	require.NoError(t, f.run(t, "--height", "9"))
	f.requireHeights(t, 10, 9, 9)
}
