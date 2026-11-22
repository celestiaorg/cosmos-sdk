package tx_test

import (
	"bytes"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/multisig"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
)

// TestGetSignaturesV2MalformedShapes checks that GetSignaturesV2 returns an
// error, and does not panic, for signature shapes that decode but are invalid.
func TestGetSignaturesV2MalformedShapes(t *testing.T) {
	cfg := moduletestutil.MakeTestEncodingConfig()
	decode := authtx.DefaultTxDecoder(cfg.Codec)

	marshal := func(m proto.Message) []byte {
		bz, err := proto.Marshal(m)
		require.NoError(t, err)
		return bz
	}
	anyOf := func(pk cryptotypes.PubKey) *codectypes.Any {
		a, err := codectypes.NewAnyWithValue(pk)
		require.NoError(t, err)
		return a
	}

	keyA := secp256k1.GenPrivKeyFromSecret([]byte("a")).PubKey()
	keyB := secp256k1.GenPrivKeyFromSecret([]byte("b")).PubKey()
	multiPK := anyOf(multisig.NewLegacyAminoPubKey(2, []cryptotypes.PubKey{keyA, keyB}))

	single := &txtypes.ModeInfo{Sum: &txtypes.ModeInfo_Single_{Single: &txtypes.ModeInfo_Single{Mode: signingtypes.SignMode_SIGN_MODE_DIRECT}}}
	bits := cryptotypes.NewCompactBitArray(2)
	bits.SetIndex(0, true)
	bits.SetIndex(1, true)
	multiWith := func(inner ...*txtypes.ModeInfo) *txtypes.ModeInfo {
		return &txtypes.ModeInfo{Sum: &txtypes.ModeInfo_Multi_{Multi: &txtypes.ModeInfo_Multi{Bitarray: bits, ModeInfos: inner}}}
	}

	sig := bytes.Repeat([]byte{7}, 64)
	twoSigs := marshal(&cryptotypes.MultiSignature{Signatures: [][]byte{sig, sig}})
	body := marshal(&txtypes.TxBody{Memo: "memo"})

	testCases := []struct {
		name        string
		signerInfos []*txtypes.SignerInfo
		sigs        [][]byte
	}{
		{
			name:        "fewer signatures than signer infos",
			signerInfos: []*txtypes.SignerInfo{{PublicKey: anyOf(keyA), ModeInfo: single, Sequence: 1}},
			sigs:        nil,
		},
		{
			name: "fewer signatures than signer infos, second signer",
			signerInfos: []*txtypes.SignerInfo{
				{PublicKey: anyOf(keyA), ModeInfo: single, Sequence: 1},
				{PublicKey: anyOf(keyB), ModeInfo: single, Sequence: 1},
			},
			sigs: [][]byte{sig},
		},
		{
			name:        "empty top-level mode info",
			signerInfos: []*txtypes.SignerInfo{{PublicKey: anyOf(keyA), ModeInfo: &txtypes.ModeInfo{}, Sequence: 1}},
			sigs:        [][]byte{sig},
		},
		{
			name:        "empty mode info nested in a multisig",
			signerInfos: []*txtypes.SignerInfo{{PublicKey: multiPK, ModeInfo: multiWith(single, &txtypes.ModeInfo{}), Sequence: 1}},
			sigs:        [][]byte{twoSigs},
		},
	}
	type sigsTx interface {
		GetSignaturesV2() ([]signingtypes.SignatureV2, error)
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			authInfo := marshal(&txtypes.AuthInfo{SignerInfos: tc.signerInfos, Fee: &txtypes.Fee{GasLimit: 100_000}})
			raw := marshal(&txtypes.TxRaw{BodyBytes: body, AuthInfoBytes: authInfo, Signatures: tc.sigs})

			decoded, err := decode(raw)
			require.NoError(t, err)
			require.NotPanics(t, func() { _, err = decoded.(sigsTx).GetSignaturesV2() })
			require.Error(t, err)
		})
	}
}
