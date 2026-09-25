package baseapp_test

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	pruningtypes "cosmossdk.io/store/pruning/types"
	"cosmossdk.io/store/snapshots"
	snapshottypes "cosmossdk.io/store/snapshots/types"

	"github.com/cosmos/cosmos-sdk/baseapp"
	baseapptestutil "github.com/cosmos/cosmos-sdk/baseapp/testutil"
	"github.com/cosmos/cosmos-sdk/testutil"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// TestBaseApp_Close_ClosesSnapshotsBeforeApplicationDB asserts shutdown order:
// snapshot manager (and any in-flight export) before application.db.
// Closing the app DB first races with SnapshotIfApplicable.
// See https://github.com/celestiaorg/celestia-app/issues/7252.
func TestBaseApp_Close_ClosesSnapshotsBeforeApplicationDB(t *testing.T) {
	snapshotStore, err := snapshots.NewStore(dbm.NewMemDB(), testutil.GetTempDir(t))
	require.NoError(t, err)

	suite := NewBaseAppSuite(t,
		baseapp.SetSnapshot(snapshotStore, snapshottypes.NewSnapshotOptions(1000, 1)),
		baseapp.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningNothing)),
	)

	suite.logBuffer.Reset()
	require.NoError(t, suite.baseApp.Close())

	logs := suite.logBuffer.String()
	snapIdx := strings.Index(logs, "Closing snapshots/metadata.db")
	appIdx := strings.Index(logs, "Closing application.db")
	require.NotEqual(t, -1, snapIdx, "expected snapshot close log, got: %s", logs)
	require.NotEqual(t, -1, appIdx, "expected application.db close log, got: %s", logs)
	require.Less(t, snapIdx, appIdx, "snapshots must close before application.db; logs:\n%s", logs)
}

// TestBaseApp_Close_WithInFlightSnapshot commits a snapshot-interval height with enough
// state that async SnapshotIfApplicable is still running, then Close() must return
// without panicking. Uses pruning-everything so Create's PruneSnapshotHeight path is
// exercised (the HandleSnapshotHeight panic in https://github.com/celestiaorg/celestia-app/issues/7252).
func TestBaseApp_Close_WithInFlightSnapshot(t *testing.T) {
	snapshotStore, err := snapshots.NewStore(dbm.NewMemDB(), testutil.GetTempDir(t))
	require.NoError(t, err)

	const (
		snapshotInterval = uint64(1)
		blockTxs         = 4
	)

	suite := NewBaseAppSuite(t,
		baseapp.SetSnapshot(snapshotStore, snapshottypes.NewSnapshotOptions(snapshotInterval, 1)),
		baseapp.SetPruning(pruningtypes.NewPruningOptions(pruningtypes.PruningEverything)),
	)
	baseapptestutil.RegisterKeyValueServer(suite.baseApp.MsgServiceRouter(), MsgKeyValueImpl{})

	_, err = suite.baseApp.InitChain(&abci.RequestInitChain{
		ConsensusParams: &cmtproto.ConsensusParams{},
	})
	require.NoError(t, err)

	r := rand.New(rand.NewSource(7252))
	_, _, addr := testdata.KeyTestPubAddr()
	txs := make([][]byte, 0, blockTxs)
	keyCounter := 0
	for txNum := 0; txNum < blockTxs; txNum++ {
		msgs := make([]sdk.Msg, 0, 100)
		for msgNum := 0; msgNum < 100; msgNum++ {
			value := make([]byte, 10000)
			_, err := r.Read(value)
			require.NoError(t, err)
			msgs = append(msgs, &baseapptestutil.MsgKeyValue{
				Key:    []byte(fmt.Sprintf("%v", keyCounter)),
				Value:  value,
				Signer: addr.String(),
			})
			keyCounter++
		}
		builder := suite.txConfig.NewTxBuilder()
		require.NoError(t, builder.SetMsgs(msgs...))
		setTxSignature(t, builder, 0)
		txBytes, err := suite.txConfig.TxEncoder()(builder.GetTx())
		require.NoError(t, err)
		txs = append(txs, txBytes)
	}

	_, err = suite.baseApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: 1,
		Txs:    txs,
	})
	require.NoError(t, err)

	// Commit triggers SnapshotIfApplicable asynchronously; do not wait for it.
	_, err = suite.baseApp.Commit()
	require.NoError(t, err)

	suite.logBuffer.Reset()
	require.NotPanics(t, func() {
		require.NoError(t, suite.baseApp.Close())
	})

	logs := suite.logBuffer.String()
	snapIdx := strings.Index(logs, "Closing snapshots/metadata.db")
	appIdx := strings.Index(logs, "Closing application.db")
	require.NotEqual(t, -1, snapIdx, "expected snapshot close log, got: %s", logs)
	require.NotEqual(t, -1, appIdx, "expected application.db close log, got: %s", logs)
	require.Less(t, snapIdx, appIdx, "snapshots must close before application.db; logs:\n%s", logs)

	// Further snapshot attempts must fail fast on the closed manager.
	_, err = suite.baseApp.SnapshotManager().Create(1)
	require.ErrorIs(t, err, snapshots.ErrAborted)
}
