package simapp

import (
	"context"
	"errors"

	abci "github.com/cometbft/cometbft/abci/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
)

// CheckTx reports the first signer and its sequence. The celestia-core CAT
// mempool gossips a tx only with its signer and disconnects a peer that sends
// a SeenTx without one.
func (app *SimApp) CheckTx(req *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
	res, err := app.BaseApp.CheckTx(req)
	if err != nil || res.Code != abci.CodeTypeOK {
		return res, err
	}

	// The tx passed the ante handler, so it decodes and is signed.
	tx, err := app.TxDecode(req.Tx)
	if err != nil {
		return nil, err
	}
	sigTx, ok := tx.(authsigning.SigVerifiableTx)
	if !ok {
		return res, nil
	}
	sigs, err := sigTx.GetSignaturesV2()
	if err != nil {
		return nil, err
	}
	if len(sigs) == 0 || sigs[0].PubKey == nil {
		return res, nil
	}

	res.Address = sigs[0].PubKey.Address()
	res.Sequence = sigs[0].Sequence
	return res, nil
}

// QuerySequence returns the signer's sequence from the check state. CAT uses
// it to decide whether a gossiped tx is next for its signer.
func (app *SimApp) QuerySequence(_ context.Context, req *abci.RequestQuerySequence) (*abci.ResponseQuerySequence, error) {
	ctx, ok := app.CheckState()
	if !ok {
		return nil, errors.New("check state not set")
	}

	seq, err := app.AccountKeeper.GetSequence(ctx, sdk.AccAddress(req.Signer))
	if errors.Is(err, sdkerrors.ErrUnknownAddress) {
		return &abci.ResponseQuerySequence{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &abci.ResponseQuerySequence{Sequence: seq}, nil
}
