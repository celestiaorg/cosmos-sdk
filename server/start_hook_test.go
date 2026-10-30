package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/node"
	"github.com/cometbft/cometbft/privval"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/gogoproto/grpc"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/snapshots"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/server/api"
	"github.com/cosmos/cosmos-sdk/server/config"
	"github.com/cosmos/cosmos-sdk/server/mock"
	"github.com/cosmos/cosmos-sdk/server/types"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
)

// hookTestApp completes the mock ABCI app into a types.Application.
type hookTestApp struct{ types.ABCI }

func (hookTestApp) RegisterAPIRoutes(*api.Server, config.APIConfig)   {}
func (hookTestApp) RegisterGRPCServer(grpc.Server)                    {}
func (hookTestApp) RegisterTxService(client.Context)                  {}
func (hookTestApp) RegisterTendermintService(client.Context)          {}
func (hookTestApp) RegisterNodeService(client.Context, config.Config) {}
func (hookTestApp) CommitMultiStore() storetypes.CommitMultiStore     { return nil }
func (hookTestApp) SnapshotManager() *snapshots.Manager               { return nil }

// InitChain hides from the bare mock app what it cannot handle (chain ID,
// params, validators) and echoes the genesis validators back to CometBFT.
func (a hookTestApp) InitChain(req *abci.RequestInitChain) (*abci.ResponseInitChain, error) {
	vals := req.Validators
	req.ChainId, req.ConsensusParams, req.Validators = "", nil, nil
	res, err := a.ABCI.InitChain(req)
	if err == nil {
		res.Validators = vals
	}
	return res, err
}

func (hookTestApp) Close() error { return nil }

func TestStartCmtNodeCallsPreNodeStart(t *testing.T) {
	cfg := newHookTestConfig(t)

	abciApp, err := mock.NewApp(cfg.RootDir, log.NewNopLogger())
	require.NoError(t, err)
	app := hookTestApp{abciApp}
	svrCtx := NewDefaultContext()

	var (
		gotApp     types.Application
		wasRunning bool
		hadMempool bool
	)
	n, cleanup, err := startCmtNode(context.Background(), cfg, app, svrCtx, func(a types.Application, n *node.Node) error {
		gotApp, wasRunning, hadMempool = a, n.IsRunning(), n.Mempool() != nil
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.Equal(t, app, gotApp)
	require.False(t, wasRunning, "hook must run before the node starts")
	require.True(t, hadMempool)
	require.True(t, n.IsRunning())
	cleanup()

	// A hook error aborts the start.
	cfg2 := newHookTestConfig(t)
	abciApp2, err := mock.NewApp(cfg2.RootDir, log.NewNopLogger())
	require.NoError(t, err)
	hookErr := errors.New("hook failed")
	n, _, err = startCmtNode(context.Background(), cfg2, hookTestApp{abciApp2}, svrCtx, func(types.Application, *node.Node) error {
		return hookErr
	})
	require.ErrorIs(t, err, hookErr)
	require.False(t, n.IsRunning())
}

// newHookTestConfig writes a single-validator genesis with the app state
// mock.InitChainer expects.
func newHookTestConfig(t *testing.T) *cmtcfg.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := cmtcfg.TestConfig().SetRoot(dir)
	cmtcfg.EnsureRoot(dir)
	cfg.P2P.ListenAddress = "tcp://127.0.0.1:0"
	cfg.RPC.ListenAddress = "tcp://127.0.0.1:0"
	pv := privval.LoadOrGenFilePV(cfg.PrivValidatorKeyFile(), cfg.PrivValidatorStateFile())
	pub, err := pv.GetPubKey()
	require.NoError(t, err)
	gen := genutiltypes.NewAppGenesisWithVersion("hook-test", json.RawMessage(`{"values":[]}`))
	gen.GenesisTime = time.Now()
	gen.Consensus.Validators = []cmttypes.GenesisValidator{{Address: pub.Address(), PubKey: pub, Power: 10, Name: "v"}}
	require.NoError(t, gen.SaveAs(cfg.GenesisFile()))
	return cfg
}
