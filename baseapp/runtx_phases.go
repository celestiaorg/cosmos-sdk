package baseapp

import (
	"context"
	"fmt"

	"github.com/cockroachdb/errors"
	abci "github.com/cometbft/cometbft/abci/types"

	errorsmod "cosmossdk.io/errors"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/mempool"
)

// preparedTx carries a tx between its ante phase (runTxAnte) and its message
// phase (runTxMsgs).
type preparedTx struct {
	tx      sdk.Tx
	txBytes []byte
	// ctx's gas meter is cumulative across both phases; after a successful
	// ante phase its multistore has the ante writes committed
	ctx        sdk.Context
	gasWanted  uint64
	anteEvents []abci.Event
	priority   int64
}

// runTxAnte runs the ante phase of a tx: decode, validation, ante handlers
// (whose writes are committed to state on success), and mempool bookkeeping.
// The returned preparedTx is never nil, so callers can report gas consumed
// even on failure.
func (app *BaseApp) runTxAnte(mode execMode, txBytes []byte) (p *preparedTx, err error) {
	ctx := app.getContextForTx(mode, txBytes)
	ms := ctx.MultiStore()

	p = &preparedTx{
		txBytes: txBytes,
		ctx:     ctx,
	}

	defer func() {
		if r := recover(); r != nil {
			recoveryMW := newOutOfGasRecoveryMiddleware(p.gasWanted, p.ctx, app.runTxRecoveryMiddleware)
			err = processRecovery(r, recoveryMW)
			p.ctx.Logger().Debug("panic recovered in runTxAnte", "err", err)
		}
	}()

	tx, err := app.txDecoder(txBytes)
	if err != nil {
		return p, err
	}

	msgs := tx.GetMsgs()
	if err := validateBasicTxMsgs(msgs); err != nil {
		return p, err
	}

	for _, msg := range msgs {
		handler := app.msgServiceRouter.Handler(msg)
		if handler == nil {
			return p, errorsmod.Wrapf(sdkerrors.ErrUnknownRequest, "no message handler found for %T", msg)
		}
	}

	p.tx = tx

	if app.anteHandler != nil {
		var (
			anteCtx sdk.Context
			msCache storetypes.CacheMultiStore
		)

		// Branch context before AnteHandler call in case it aborts.
		// This is required for both CheckTx and DeliverTx.
		// Ref: https://github.com/cosmos/cosmos-sdk/issues/2772
		//
		// NOTE: Alternatively, we could require that AnteHandler ensures that
		// writes do not happen if aborted/failed.  This may have some
		// performance benefits, but it'll be more difficult to get right.
		anteCtx, msCache = app.cacheTxContext(p.ctx, txBytes)
		anteCtx = anteCtx.WithEventManager(sdk.NewEventManager())
		newCtx, err := app.anteHandler(anteCtx, tx, mode == execModeSimulate)

		if !newCtx.IsZero() {
			// At this point, newCtx.MultiStore() is a store branch, or something else
			// replaced by the AnteHandler. We want the original multistore.
			//
			// Also, in the case of the tx aborting, we need to track gas consumed via
			// the instantiated gas meter in the AnteHandler, so we update the context
			// prior to returning.
			p.ctx = newCtx.WithMultiStore(ms)
		}

		events := p.ctx.EventManager().Events()

		// GasMeter expected to be set in AnteHandler
		p.gasWanted = p.ctx.GasMeter().Limit()

		if err != nil {
			if mode == execModeReCheck {
				// if the ante handler fails on recheck, we want to remove the tx from the mempool
				if mempoolErr := app.mempool.Remove(tx); mempoolErr != nil {
					return p, errors.Join(err, mempoolErr)
				}
			}
			return p, err
		}

		msCache.Write()
		p.anteEvents = events.ToABCIEvents()
		p.priority = p.ctx.Priority()
	}

	switch mode {
	case execModeCheck:
		if err := app.mempool.Insert(p.ctx, tx); err != nil {
			return p, err
		}
	case execModeFinalize:
		if err := app.mempool.Remove(tx); err != nil && !errors.Is(err, mempool.ErrTxNotFound) {
			return p, fmt.Errorf("failed to remove tx from mempool: %w", err)
		}
	}

	return p, nil
}

// runTxMsgs runs the message phase of a prepared tx: messages and optional
// post handlers execute on a multistore branch, committed only on success.
func (app *BaseApp) runTxMsgs(mode execMode, p *preparedTx) (result *sdk.Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			recoveryMW := newOutOfGasRecoveryMiddleware(p.gasWanted, p.ctx, app.runTxRecoveryMiddleware)
			err, result = processRecovery(r, recoveryMW), nil
			p.ctx.Logger().Debug("panic recovered in runTx", "err", err)
		}
	}()

	// Create a new Context based off of the existing Context with a MultiStore branch
	// in case message processing fails. At this point, the MultiStore
	// is a branch of a branch.
	runMsgCtx, msCache := app.cacheTxContext(p.ctx, p.txBytes)

	// Attempt to execute all messages and only update state if all messages pass
	// and we're in DeliverTx. Note, runMsgs will never return a reference to a
	// Result if any single message fails or does not have a registered Handler.
	msgsV2, err := p.tx.GetMsgsV2()
	if err == nil {
		result, err = app.runMsgs(runMsgCtx, p.tx.GetMsgs(), msgsV2, mode)
	}

	// Run optional postHandlers (should run regardless of the execution result).
	//
	// Note: If the postHandler fails, we also revert the runMsgs state.
	if app.postHandler != nil {
		// The runMsgCtx context currently contains events emitted by the ante handler.
		// We clear this to correctly order events without duplicates.
		// Note that the state is still preserved.
		postCtx := runMsgCtx.WithEventManager(sdk.NewEventManager())

		newCtx, errPostHandler := app.postHandler(postCtx, p.tx, mode == execModeSimulate, err == nil)
		if errPostHandler != nil {
			return nil, errors.Join(err, errPostHandler)
		}

		// we don't want runTx to panic if runMsgs has failed earlier
		if result == nil {
			result = &sdk.Result{}
		}
		result.Events = append(result.Events, newCtx.EventManager().ABCIEvents()...)
	}

	if err == nil {
		if mode == execModeFinalize {
			msCache.Write()
		}

		if len(p.anteEvents) > 0 && (mode == execModeFinalize || mode == execModeSimulate) {
			// append the events in the order of occurrence
			result.Events = append(p.anteEvents, result.Events...)
		}
	}

	return result, err
}

// phasedTxResult builds the ExecTxResult for a phased tx. result is nil when
// the tx failed; err says why.
func (app *BaseApp) phasedTxResult(p *preparedTx, result *sdk.Result, err error) *abci.ExecTxResult {
	// the gas meter in p.ctx is cumulative across the ante and message phases
	gInfo := sdk.GasInfo{GasWanted: p.gasWanted, GasUsed: p.ctx.GasMeter().GasConsumed()}
	resultStr := "successful"

	defer func() {
		telemetry.IncrCounter(1, "tx", "count")
		telemetry.IncrCounter(1, "tx", resultStr)
		telemetry.SetGauge(float32(gInfo.GasUsed), "tx", "gas", "used")
		telemetry.SetGauge(float32(gInfo.GasWanted), "tx", "gas", "wanted")
	}()

	signers, signerErr := app.extractSigners(p.txBytes)
	if signerErr != nil {
		// log but don't fail the tx
		app.logger.Error("failed to extract signers", "error", signerErr)
	}

	if err != nil {
		resultStr = "failed"
		resp := sdkerrors.ResponseExecTxResultWithEvents(
			err,
			gInfo.GasWanted,
			gInfo.GasUsed,
			sdk.MarkEventsToIndex(p.anteEvents, app.indexEvents),
			app.trace,
		)
		resp.Signers = signers
		return resp
	}

	return &abci.ExecTxResult{
		GasWanted: int64(gInfo.GasWanted),
		GasUsed:   int64(gInfo.GasUsed),
		Log:       result.Log,
		Data:      result.Data,
		Events:    sdk.MarkEventsToIndex(result.Events, app.indexEvents),
		Signers:   signers,
	}
}

// executeTxsPhased executes the block's transactions in two phases: first the
// ante handlers of every tx, then the messages of every tx that passed ante,
// both in canonical order.
//
// NOTE: Phased execution does not consume block gas, so a finite MaxGas is
// not enforced: txs are limited only by their own gas limits.
func (app *BaseApp) executeTxsPhased(ctx context.Context, txs [][]byte) ([]*abci.ExecTxResult, error) {
	passedAnte := make([]*preparedTx, len(txs))
	execTxResults := make([]*abci.ExecTxResult, len(txs))

	// Phase 1: ante handlers. Txs that fail here get their final result
	// immediately; the rest get theirs in phase 2.
	for i, rawTx := range txs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if _, err := app.txDecoder(rawTx); err != nil {
			// comet expects a response for every tx in the block, even
			// malformed ones (e.g. injected vote extensions)
			execTxResults[i] = sdkerrors.ResponseExecTxResultWithEvents(
				sdkerrors.ErrTxDecode,
				0,
				0,
				nil,
				false,
			)
			continue
		}

		p, err := app.runTxAnte(execModeFinalize, rawTx)
		if err != nil {
			execTxResults[i] = app.phasedTxResult(p, nil, err)
			continue
		}

		passedAnte[i] = p
	}

	// Phase 2: messages. The ante writes (fee deduction, sequence increment)
	// from phase 1 are retained even when the messages fail.
	for i, p := range passedAnte {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if p == nil {
			// the tx already has its result from phase 1
			continue
		}

		result, err := app.runTxMsgs(execModeFinalize, p)
		execTxResults[i] = app.phasedTxResult(p, result, err)
	}

	return execTxResults, nil
}
