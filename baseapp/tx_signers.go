package baseapp

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/signing"
)

// ExtractTxSigners extracts the signers from a transaction.
// It returns the list of signer addresses as byte slices, or an error if
// the transaction doesn't implement the necessary interfaces.
func ExtractTxSigners(tx sdk.Tx) ([][]byte, error) {
	sigTx, ok := tx.(signing.SigVerifiableTx)
	if !ok {
		// If the transaction doesn't implement SigVerifiableTx, return empty slice
		return [][]byte{}, nil
	}

	signers, err := sigTx.GetSigners()
	if err != nil {
		return nil, err
	}

	return signers, nil
}