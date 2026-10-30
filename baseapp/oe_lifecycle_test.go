package baseapp

import (
	"context"
	"sync/atomic"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/baseapp/oe"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

type oeCloseDB struct {
	dbm.DB
	workerFinished *atomic.Bool
	closedTooEarly bool
}

func (db *oeCloseDB) Close() error {
	db.closedTooEarly = !db.workerFinished.Load()
	return db.DB.Close()
}

// Consensus can stop after ProcessProposal returns while its speculative worker
// is still reading application state. Close must join that worker before the DB.
func TestCloseWaitsForOptimisticExecution(t *testing.T) {
	var finished atomic.Bool
	started := make(chan struct{})
	db := &oeCloseDB{DB: dbm.NewMemDB(), workerFinished: &finished}
	app := NewBaseApp(t.Name(), log.NewNopLogger(), db, nil)
	app.optimisticExec = oe.NewOptimisticExecution(log.NewNopLogger(), func(ctx context.Context, _ *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
		close(started)
		<-ctx.Done()
		finished.Store(true)
		return nil, ctx.Err()
	})
	t.Cleanup(app.optimisticExec.Abort)
	app.optimisticExec.Execute(&abci.RequestProcessProposal{Height: 2, Hash: []byte("h2")})
	<-started

	require.NoError(t, app.Close())
	require.False(t, db.closedTooEarly, "application DB closed while speculative execution was still using it")
}

// A consumed result must not be served again when consensus reaches the next
// height directly through FinalizeBlock, e.g. block sync, without a proposal.
func TestFinalizeBlockConsumesOptimisticExecution(t *testing.T) {
	var heights []int64
	app := NewBaseApp(t.Name(), log.NewNopLogger(), dbm.NewMemDB(), nil, SetOptimisticExecution())
	app.SetPreBlocker(func(_ sdk.Context, req *abci.RequestFinalizeBlock) (*sdk.ResponsePreBlock, error) {
		heights = append(heights, req.Height)
		return &sdk.ResponsePreBlock{}, nil
	})
	require.NoError(t, app.LoadLatestVersion())
	_, err := app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: 1})
	require.NoError(t, err)
	_, err = app.Commit()
	require.NoError(t, err)

	_, err = app.ProcessProposal(&abci.RequestProcessProposal{Height: 2})
	require.NoError(t, err)
	_, err = app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: 2})
	require.NoError(t, err)
	require.False(t, app.optimisticExec.Initialized(), "consumed speculative result retained")
	_, err = app.Commit()
	require.NoError(t, err)
	_, err = app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: 3})
	require.NoError(t, err)
	_, err = app.Commit()
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 3}, heights)
}
