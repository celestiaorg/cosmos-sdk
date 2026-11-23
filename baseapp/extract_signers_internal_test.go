package baseapp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
)

// sigsTx is a SigVerifiableTx whose GetSignaturesV2 returns sigs, or panics
// when panicMsg is set.
type sigsTx struct {
	sdk.Tx
	sigs     []signing.SignatureV2
	panicMsg string
}

func (sigsTx) GetSigners() ([][]byte, error)             { return nil, nil }
func (sigsTx) GetPubKeys() ([]cryptotypes.PubKey, error) { return nil, nil }

func (s sigsTx) GetSignaturesV2() ([]signing.SignatureV2, error) {
	if s.panicMsg != "" {
		panic(s.panicMsg)
	}
	return s.sigs, nil
}

// TestExtractTxSignersRecoversPanics checks that extractTxSigners returns an
// error, and does not panic, when reading the signers of a malformed tx panics.
// FinalizeBlock calls it outside any recover, so a panic would halt the node.
func TestExtractTxSignersRecoversPanics(t *testing.T) {
	app := &BaseApp{}

	t.Run("secp256k1 public key of the wrong length", func(t *testing.T) {
		tx := sigsTx{sigs: []signing.SignatureV2{{PubKey: &secp256k1.PubKey{Key: []byte{1, 2, 3}}}}}
		var err error
		require.NotPanics(t, func() { _, err = app.extractTxSigners(tx) })
		require.Error(t, err)
	})

	t.Run("GetSignaturesV2 panics", func(t *testing.T) {
		tx := sigsTx{panicMsg: "boom"}
		var err error
		require.NotPanics(t, func() { _, err = app.extractTxSigners(tx) })
		require.ErrorContains(t, err, "boom")
	})

	t.Run("valid public key", func(t *testing.T) {
		pk := secp256k1.GenPrivKeyFromSecret([]byte("a")).PubKey()
		signers, err := app.extractTxSigners(sigsTx{sigs: []signing.SignatureV2{{PubKey: pk}}})
		require.NoError(t, err)
		require.Equal(t, []string{sdk.AccAddress(pk.Address()).String()}, signers)
	})
}
