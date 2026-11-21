package baseapp

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// SetBatchSigPreverifier installs an optional hook that is given all txs of a
// DeliverTx batch before they execute (sync-speedup prototype). The hook may
// only warm caches: it runs on a discarded branch of the deliver state with an
// infinite gas meter, so it cannot change state or gas accounting.
func (app *BaseApp) SetBatchSigPreverifier(f func(ctx sdk.Context, txs []sdk.Tx)) {
	app.batchSigPreverifier = f
}

// PrepareDeliverTxBatch is called by the ABCI server before a batch of
// DeliverTx calls for the current block.
func (app *BaseApp) PrepareDeliverTxBatch(rawTxs [][]byte) {
	if app.batchSigPreverifier == nil || app.deliverState == nil || len(rawTxs) < 2 {
		return
	}
	txs := make([]sdk.Tx, 0, len(rawTxs))
	for _, b := range rawTxs {
		if tx, err := app.txDecoder(b); err == nil {
			txs = append(txs, tx)
		}
	}
	ctx, _ := app.deliverState.ctx.CacheContext()
	ctx = ctx.WithGasMeter(sdk.NewInfiniteGasMeter())
	func() {
		defer func() { _ = recover() }() // best effort: never affect execution
		app.batchSigPreverifier(ctx, txs)
	}()
}
