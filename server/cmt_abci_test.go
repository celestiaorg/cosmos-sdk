package server

import (
	"context"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/stretchr/testify/require"

	servertypes "github.com/cosmos/cosmos-sdk/server/types"
)

type recheckTxsApp struct {
	servertypes.ABCI
}

func (recheckTxsApp) RecheckTxs(_ context.Context, req *abci.RequestRecheckTxs) (*abci.ResponseRecheckTxs, error) {
	return &abci.ResponseRecheckTxs{Responses: make([]*abci.ResponseCheckTx, len(req.Txs))}, nil
}

func TestCometABCIWrapperRecheckTxs(t *testing.T) {
	ctx := context.Background()
	req := &abci.RequestRecheckTxs{Txs: [][]byte{[]byte("a"), []byte("b")}}

	w, ok := NewCometABCIWrapper(recheckTxsApp{}).(abci.RecheckTxsApplication)
	require.True(t, ok, "the standalone wrapper must expose RecheckTxs")
	rsp, err := w.RecheckTxs(ctx, req)
	require.NoError(t, err)
	require.Len(t, rsp.Responses, 2)

	var plain struct{ servertypes.ABCI }
	_, err = NewCometABCIWrapper(plain).(abci.RecheckTxsApplication).RecheckTxs(ctx, req)
	require.ErrorIs(t, err, abci.ErrRecheckTxsUnsupported)
}
