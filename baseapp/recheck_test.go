package baseapp_test

import (
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/baseapp"
	baseapptestutil "github.com/cosmos/cosmos-sdk/baseapp/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

type recheckAnteCall struct {
	eventsDiscarded bool
	maxBytes        int64
}

func newRecheckSuite(t *testing.T, opts ...func(*baseapp.BaseApp)) (*BaseAppSuite, *[]recheckAnteCall) {
	t.Helper()
	calls := &[]recheckAnteCall{}
	anteOpt := func(bapp *baseapp.BaseApp) {
		bapp.SetAnteHandler(func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) {
			*calls = append(*calls, recheckAnteCall{sdk.EventsDiscarded(ctx), ctx.ConsensusParams().Block.MaxBytes})
			return ctx, nil
		})
	}
	suite := NewBaseAppSuite(t, append(opts, anteOpt)...)
	baseapptestutil.RegisterCounterServer(suite.baseApp.MsgServiceRouter(), NoopCounterServerImpl{})
	_, err := suite.baseApp.InitChain(&abci.RequestInitChain{
		ConsensusParams: &cmtproto.ConsensusParams{Block: &cmtproto.BlockParams{MaxBytes: 1, MaxGas: -1}},
	})
	require.NoError(t, err)
	return suite, calls
}

func checkTx(t *testing.T, app *baseapp.BaseApp, txBytes []byte, typ abci.CheckTxType) *abci.ResponseCheckTx {
	t.Helper()
	res, err := app.CheckTx(&abci.RequestCheckTx{Tx: txBytes, Type: typ})
	require.NoError(t, err)
	return res
}

func TestRecheckSkipsValidateBasic(t *testing.T) {
	suite, _ := newRecheckSuite(t)
	txBytes, err := suite.txConfig.TxEncoder()(newTxCounter(t, suite.txConfig, 0, -1))
	require.NoError(t, err)

	require.Contains(t, checkTx(t, suite.baseApp, txBytes, abci.CheckTxType_New).Log, "non-negative")
	// Recheck relies on the ValidateBasic CheckTx ran when it admitted the tx.
	require.True(t, checkTx(t, suite.baseApp, txBytes, abci.CheckTxType_Recheck).IsOK())
}

func TestRecheckDiscardsAnteEvents(t *testing.T) {
	suite, calls := newRecheckSuite(t)
	txBytes, err := suite.txConfig.TxEncoder()(newTxCounter(t, suite.txConfig, 0, 1))
	require.NoError(t, err)

	require.True(t, checkTx(t, suite.baseApp, txBytes, abci.CheckTxType_New).IsOK())
	require.True(t, checkTx(t, suite.baseApp, txBytes, abci.CheckTxType_Recheck).IsOK())
	require.Equal(t, []bool{false, true}, []bool{(*calls)[0].eventsDiscarded, (*calls)[1].eventsDiscarded})
}

func TestCheckTxReadsConsensusParamsOncePerCheckState(t *testing.T) {
	suite, calls := newRecheckSuite(t)
	app := suite.baseApp
	txBytes, err := suite.txConfig.TxEncoder()(newTxCounter(t, suite.txConfig, 0, 1))
	require.NoError(t, err)

	checkTx(t, app, txBytes, abci.CheckTxType_New)
	// The test param store ignores ctx, so this write is visible to every state.
	require.NoError(t, app.StoreConsensusParams(sdk.Context{}, cmtproto.ConsensusParams{Block: &cmtproto.BlockParams{MaxBytes: 2, MaxGas: -1}}))
	checkTx(t, app, txBytes, abci.CheckTxType_Recheck)

	_, err = app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: 1})
	require.NoError(t, err)
	_, err = app.Commit()
	require.NoError(t, err)
	checkTx(t, app, txBytes, abci.CheckTxType_Recheck)

	maxBytes := make([]int64, len(*calls))
	for i, c := range *calls {
		maxBytes[i] = c.maxBytes
	}
	require.Equal(t, []int64{1, 1, 2}, maxBytes)
}

func TestRecheckRunsPostHandler(t *testing.T) {
	postRuns := 0
	suite, _ := newRecheckSuite(t, func(bapp *baseapp.BaseApp) {
		bapp.SetPostHandler(func(ctx sdk.Context, _ sdk.Tx, _, _ bool) (sdk.Context, error) {
			postRuns++
			return ctx, nil
		})
	})
	txBytes, err := suite.txConfig.TxEncoder()(newTxCounter(t, suite.txConfig, 0, 1))
	require.NoError(t, err)

	require.True(t, checkTx(t, suite.baseApp, txBytes, abci.CheckTxType_Recheck).IsOK())
	require.Equal(t, 1, postRuns)
}
