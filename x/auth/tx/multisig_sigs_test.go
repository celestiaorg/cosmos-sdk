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

// TestGetSignaturesV2MultisigSignatureCount checks that GetSignaturesV2
// returns an error, and does not panic, when a 2-of-2 multisig signer's
// MultiSignature holds fewer signatures than mode infos.
func TestGetSignaturesV2MultisigSignatureCount(t *testing.T) {
	cfg := moduletestutil.MakeTestEncodingConfig()
	decode := authtx.DefaultTxDecoder(cfg.Codec)

	keys := []cryptotypes.PubKey{
		secp256k1.GenPrivKeyFromSecret([]byte("a")).PubKey(),
		secp256k1.GenPrivKeyFromSecret([]byte("b")).PubKey(),
	}
	pk, err := codectypes.NewAnyWithValue(multisig.NewLegacyAminoPubKey(2, keys))
	require.NoError(t, err)

	single := &txtypes.ModeInfo{Sum: &txtypes.ModeInfo_Single_{Single: &txtypes.ModeInfo_Single{Mode: signingtypes.SignMode_SIGN_MODE_DIRECT}}}
	bits := cryptotypes.NewCompactBitArray(2)
	bits.SetIndex(0, true)
	bits.SetIndex(1, true)
	mode := &txtypes.ModeInfo{Sum: &txtypes.ModeInfo_Multi_{Multi: &txtypes.ModeInfo_Multi{Bitarray: bits, ModeInfos: []*txtypes.ModeInfo{single, single}}}}

	marshal := func(m proto.Message) []byte {
		bz, err := proto.Marshal(m)
		require.NoError(t, err)
		return bz
	}
	body := marshal(&txtypes.TxBody{Memo: "memo"})
	authInfo := marshal(&txtypes.AuthInfo{
		SignerInfos: []*txtypes.SignerInfo{{PublicKey: pk, ModeInfo: mode, Sequence: 1}},
		Fee:         &txtypes.Fee{GasLimit: 100_000},
	})

	sigA, sigB := bytes.Repeat([]byte{7}, 64), bytes.Repeat([]byte{8}, 64)
	testCases := []struct {
		name   string
		sigs   [][]byte
		wantOK bool
	}{
		{"no signatures", nil, false},
		{"one signature", [][]byte{sigA}, false},
		{"two signatures", [][]byte{sigA, sigB}, true},
	}
	type sigsTx interface {
		GetSignaturesV2() ([]signingtypes.SignatureV2, error)
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			multiSig := marshal(&cryptotypes.MultiSignature{Signatures: tc.sigs})
			raw := marshal(&txtypes.TxRaw{BodyBytes: body, AuthInfoBytes: authInfo, Signatures: [][]byte{multiSig}})

			decoded, err := decode(raw)
			require.NoError(t, err)
			var sigs []signingtypes.SignatureV2
			require.NotPanics(t, func() { sigs, err = decoded.(sigsTx).GetSignaturesV2() })
			if tc.wantOK {
				require.NoError(t, err)
				require.Len(t, sigs, 1)
			} else {
				require.Error(t, err)
			}
		})
	}
}
