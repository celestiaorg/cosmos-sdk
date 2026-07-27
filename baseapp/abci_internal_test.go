package baseapp

import (
	"context"
	"errors"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"

	"cosmossdk.io/log"

	baseapptestutil "github.com/cosmos/cosmos-sdk/baseapp/testutil"
	codectestutil "github.com/cosmos/cosmos-sdk/codec/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// cancellingTx is an sdk.Tx that cancels a context during its message phase,
// simulating an optimistic execution abort mid-tx.
type cancellingTx struct {
	msgs   []sdk.Msg
	cancel context.CancelFunc
}

func (tx cancellingTx) GetMsgs() []sdk.Msg { return tx.msgs }

func (tx cancellingTx) GetMsgsV2() ([]protov2.Message, error) {
	tx.cancel()
	return nil, errors.New("tx canceled")
}

type noopCounterServer struct{}

func (noopCounterServer) IncrementCounter(context.Context, *baseapptestutil.MsgCounter) (*baseapptestutil.MsgCreateCounterResponse, error) {
	return &baseapptestutil.MsgCreateCounterResponse{}, nil
}

// TestInternalFinalizeBlockCancelledDuringFinalTx verifies that a cancellation
// arriving during the block's final tx aborts before EndBlock runs.
func TestInternalFinalizeBlockCancelledDuringFinalTx(t *testing.T) {
	cdc := codectestutil.CodecOptions{}.NewCodec()
	baseapptestutil.RegisterInterfaces(cdc.InterfaceRegistry())

	ctx, cancel := context.WithCancel(context.Background())
	tx := cancellingTx{msgs: []sdk.Msg{&baseapptestutil.MsgCounter{}}, cancel: cancel}

	app := NewBaseApp(t.Name(), log.NewNopLogger(), dbm.NewMemDB(), func([]byte) (sdk.Tx, error) {
		return tx, nil
	})
	app.SetInterfaceRegistry(cdc.InterfaceRegistry())
	app.MsgServiceRouter().SetInterfaceRegistry(cdc.InterfaceRegistry())
	baseapptestutil.RegisterCounterServer(app.MsgServiceRouter(), noopCounterServer{})

	endBlockerRan := false
	app.SetEndBlocker(func(sdk.Context) (sdk.EndBlock, error) {
		endBlockerRan = true
		return sdk.EndBlock{}, nil
	})
	require.NoError(t, app.LoadLatestVersion())

	resp, err := app.internalFinalizeBlock(ctx, &abci.RequestFinalizeBlock{
		Height: 1,
		Txs:    [][]byte{[]byte("tx")},
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, resp)
	require.False(t, endBlockerRan, "EndBlock ran for an aborted block")
}
