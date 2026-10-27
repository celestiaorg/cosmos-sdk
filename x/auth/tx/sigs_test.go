package tx

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/multisig"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
)

func TestDecodeMultisignatures(t *testing.T) {
	testSigs := [][]byte{
		[]byte("dummy1"),
		[]byte("dummy2"),
		[]byte("dummy3"),
	}

	badMultisig := testdata.BadMultiSignature{
		Signatures:     testSigs,
		MaliciousField: []byte("bad stuff..."),
	}
	bz, err := badMultisig.Marshal()
	require.NoError(t, err)

	_, err = decodeMultisignatures(bz)
	require.Error(t, err)

	goodMultisig := types.MultiSignature{
		Signatures: testSigs,
	}
	bz, err = goodMultisig.Marshal()
	require.NoError(t, err)

	decodedSigs, err := decodeMultisignatures(bz)
	require.NoError(t, err)

	require.Equal(t, testSigs, decodedSigs)
}

func singleModeInfo() *txtypes.ModeInfo {
	return &txtypes.ModeInfo{Sum: &txtypes.ModeInfo_Single_{Single: &txtypes.ModeInfo_Single{Mode: signingtypes.SignMode_SIGN_MODE_DIRECT}}}
}

func multiModeInfo(modeInfos ...*txtypes.ModeInfo) *txtypes.ModeInfo {
	return &txtypes.ModeInfo{Sum: &txtypes.ModeInfo_Multi_{Multi: &txtypes.ModeInfo_Multi{
		Bitarray:  types.NewCompactBitArray(len(modeInfos)),
		ModeInfos: modeInfos,
	}}}
}

func marshalMultisig(t *testing.T, sigs ...[]byte) []byte {
	t.Helper()
	bz, err := (&types.MultiSignature{Signatures: sigs}).Marshal()
	require.NoError(t, err)
	return bz
}

func TestModeInfoAndSigToSignatureDataMalformed(t *testing.T) {
	testCases := map[string]struct {
		modeInfo *txtypes.ModeInfo
		sig      []byte
	}{
		"nil mode info":            {nil, []byte("sig")},
		"nil sum":                  {&txtypes.ModeInfo{}, []byte("sig")},
		"multi: fewer sigs":        {multiModeInfo(singleModeInfo(), singleModeInfo()), marshalMultisig(t, []byte("sig"))},
		"multi: nil sum mode info": {multiModeInfo(&txtypes.ModeInfo{}), marshalMultisig(t, []byte("sig"))},
		"multi: nil mode info":     {multiModeInfo(nil), marshalMultisig(t, []byte("sig"))},
		"multi: nested fewer sigs": {multiModeInfo(multiModeInfo(singleModeInfo())), marshalMultisig(t, marshalMultisig(t))},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := ModeInfoAndSigToSignatureData(tc.modeInfo, tc.sig)
			require.Error(t, err)
		})
	}
}

// More sub-signatures than ModeInfos still converts and verifies cryptographically
// (the ante rejects it later in SetPubKeyDecorator, unchanged by this fix).
func TestModeInfoAndSigToSignatureDataMultisigExtraSigs(t *testing.T) {
	msg := []byte("sign bytes")
	privs := []*secp256k1.PrivKey{secp256k1.GenPrivKey(), secp256k1.GenPrivKey(), secp256k1.GenPrivKey()}
	pks := make([]types.PubKey, len(privs))
	for i, p := range privs {
		pks[i] = p.PubKey()
	}
	multiPk := multisig.NewLegacyAminoPubKey(2, pks)

	sig0, err := privs[0].Sign(msg)
	require.NoError(t, err)
	sig1, err := privs[1].Sign(msg)
	require.NoError(t, err)

	modeInfo := multiModeInfo(singleModeInfo(), singleModeInfo())
	bitarray := types.NewCompactBitArray(3)
	bitarray.SetIndex(0, true)
	bitarray.SetIndex(1, true)
	modeInfo.GetMulti().Bitarray = bitarray

	data, err := ModeInfoAndSigToSignatureData(modeInfo, marshalMultisig(t, sig0, sig1, []byte("junk")))
	require.NoError(t, err)
	multiData := data.(*signingtypes.MultiSignatureData)
	require.Len(t, multiData.Signatures, 3)
	require.Nil(t, multiData.Signatures[2])
	getSignBytes := func(signingtypes.SignMode) ([]byte, error) { return msg, nil }
	require.NoError(t, multiPk.VerifyMultisignature(getSignBytes, multiData))
}

func TestGetSignaturesV2Malformed(t *testing.T) {
	decoder := DefaultTxDecoder(codec.NewProtoCodec(codectypes.NewInterfaceRegistry()))
	decode := func(t *testing.T, signerInfos []*txtypes.SignerInfo, sigs [][]byte) *wrapper {
		t.Helper()
		authInfoBz, err := (&txtypes.AuthInfo{SignerInfos: signerInfos, Fee: &txtypes.Fee{GasLimit: 1}}).Marshal()
		require.NoError(t, err)
		bodyBz, err := (&txtypes.TxBody{}).Marshal()
		require.NoError(t, err)
		txBz, err := (&txtypes.TxRaw{BodyBytes: bodyBz, AuthInfoBytes: authInfoBz, Signatures: sigs}).Marshal()
		require.NoError(t, err)
		decoded, err := decoder(txBz)
		require.NoError(t, err)
		return decoded.(*wrapper)
	}

	t.Run("more signer infos than signatures", func(t *testing.T) {
		w := decode(t, []*txtypes.SignerInfo{{ModeInfo: singleModeInfo()}, {ModeInfo: singleModeInfo()}}, [][]byte{[]byte("sig")})
		_, err := w.GetSignaturesV2()
		require.Error(t, err)
	})
	t.Run("nil sum mode info", func(t *testing.T) {
		w := decode(t, []*txtypes.SignerInfo{{ModeInfo: &txtypes.ModeInfo{}}}, [][]byte{[]byte("sig")})
		_, err := w.GetSignaturesV2()
		require.Error(t, err)
	})
	t.Run("multisig with fewer sub-signatures", func(t *testing.T) {
		modeInfo := multiModeInfo(singleModeInfo(), singleModeInfo())
		w := decode(t, []*txtypes.SignerInfo{{ModeInfo: modeInfo}}, [][]byte{marshalMultisig(t, []byte("sig"))})
		_, err := w.GetSignaturesV2()
		require.Error(t, err)
	})
	// unchanged shapes: unset ModeInfo needs no signature (simulation), extra signatures are ignored
	t.Run("unset mode info without signatures", func(t *testing.T) {
		w := decode(t, []*txtypes.SignerInfo{{}, {}}, nil)
		sigs, err := w.GetSignaturesV2()
		require.NoError(t, err)
		require.Len(t, sigs, 2)
		require.Nil(t, sigs[0].Data)
	})
	t.Run("more signatures than signer infos", func(t *testing.T) {
		w := decode(t, []*txtypes.SignerInfo{{ModeInfo: singleModeInfo(), Sequence: 7}}, [][]byte{[]byte("sig"), []byte("extra")})
		sigs, err := w.GetSignaturesV2()
		require.NoError(t, err)
		require.Len(t, sigs, 1)
		require.Equal(t, uint64(7), sigs[0].Sequence)
	})
}
