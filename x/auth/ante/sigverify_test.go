package ante_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"

	storetypes "cosmossdk.io/store/types"
	txsigning "cosmossdk.io/x/tx/signing"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	kmultisig "github.com/cosmos/cosmos-sdk/crypto/keys/multisig"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256r1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/crypto/types/multisig"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	"github.com/cosmos/cosmos-sdk/x/auth/migrations/legacytx"
	authsign "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	txmodule "github.com/cosmos/cosmos-sdk/x/auth/tx/config"
	"github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

func TestSetPubKey(t *testing.T) {
	suite := SetupTestSuite(t, true)
	suite.txBuilder = suite.clientCtx.TxConfig.NewTxBuilder()

	// keys and addresses
	priv1, pub1, addr1 := testdata.KeyTestPubAddr()
	priv2, pub2, addr2 := testdata.KeyTestPubAddr()
	priv3, pub3, addr3 := testdata.KeyTestPubAddrSecp256R1(t)

	addrs := []sdk.AccAddress{addr1, addr2, addr3}
	pubs := []cryptotypes.PubKey{pub1, pub2, pub3}

	msgs := make([]sdk.Msg, len(addrs))
	// set accounts and create msg for each address
	for i, addr := range addrs {
		acc := suite.accountKeeper.NewAccountWithAddress(suite.ctx, addr)
		require.NoError(t, acc.SetAccountNumber(uint64(i+1000)))
		suite.accountKeeper.SetAccount(suite.ctx, acc)
		msgs[i] = testdata.NewTestMsg(addr)
	}
	require.NoError(t, suite.txBuilder.SetMsgs(msgs...))
	suite.txBuilder.SetFeeAmount(testdata.NewTestFeeAmount())
	suite.txBuilder.SetGasLimit(testdata.NewTestGasLimit())

	privs, accNums, accSeqs := []cryptotypes.PrivKey{priv1, priv2, priv3}, []uint64{0, 1, 2}, []uint64{0, 0, 0}
	tx, err := suite.CreateTestTx(suite.ctx, privs, accNums, accSeqs, suite.ctx.ChainID(), signing.SignMode_SIGN_MODE_DIRECT)
	require.NoError(t, err)

	spkd := ante.NewSetPubKeyDecorator(suite.accountKeeper)
	antehandler := sdk.ChainAnteDecorators(spkd)

	ctx, err := antehandler(suite.ctx, tx, false)
	require.NoError(t, err)

	// Require that all accounts have pubkey set after Decorator runs
	for i, addr := range addrs {
		pk, err := suite.accountKeeper.GetPubKey(ctx, addr)
		require.NoError(t, err, "Error on retrieving pubkey from account")
		require.True(t, pubs[i].Equals(pk),
			"Wrong Pubkey retrieved from AccountKeeper, idx=%d\nexpected=%s\n     got=%s", i, pubs[i], pk)
	}
}

func TestConsumeSignatureVerificationGas(t *testing.T) {
	suite := SetupTestSuite(t, true)
	params := types.DefaultParams()
	msg := []byte{1, 2, 3, 4}

	p := types.DefaultParams()
	skR1, _ := secp256r1.GenPrivKey()
	pkSet1, sigSet1 := generatePubKeysAndSignatures(5, msg, false)
	multisigKey1 := kmultisig.NewLegacyAminoPubKey(2, pkSet1)
	multisignature1 := multisig.NewMultisig(len(pkSet1))
	expectedCost1 := expectedGasCostByKeys(pkSet1)
	for i := 0; i < len(pkSet1); i++ {
		stdSig := legacytx.StdSignature{PubKey: pkSet1[i], Signature: sigSet1[i]} //nolint:staticcheck // SA1019: legacytx.StdSignature is deprecated
		sigV2, err := legacytx.StdSignatureToSignatureV2(suite.clientCtx.LegacyAmino, stdSig)
		require.NoError(t, err)
		err = multisig.AddSignatureV2(multisignature1, sigV2, pkSet1)
		require.NoError(t, err)
	}

	type args struct {
		meter  storetypes.GasMeter
		sig    signing.SignatureData
		pubkey cryptotypes.PubKey
		params types.Params
	}
	tests := []struct {
		name        string
		args        args
		gasConsumed uint64
		shouldErr   bool
	}{
		{"PubKeyEd25519", args{storetypes.NewInfiniteGasMeter(), nil, ed25519.GenPrivKey().PubKey(), params}, p.SigVerifyCostED25519, true},
		{"PubKeySecp256k1", args{storetypes.NewInfiniteGasMeter(), nil, secp256k1.GenPrivKey().PubKey(), params}, p.SigVerifyCostSecp256k1, false},
		{"PubKeySecp256r1", args{storetypes.NewInfiniteGasMeter(), nil, skR1.PubKey(), params}, p.SigVerifyCostSecp256r1(), false},
		{"Multisig", args{storetypes.NewInfiniteGasMeter(), multisignature1, multisigKey1, params}, expectedCost1, false},
		{"unknown key", args{storetypes.NewInfiniteGasMeter(), nil, nil, params}, 0, true},
	}
	for _, tt := range tests {
		sigV2 := signing.SignatureV2{
			PubKey:   tt.args.pubkey,
			Data:     tt.args.sig,
			Sequence: 0, // Arbitrary account sequence
		}
		err := ante.DefaultSigVerificationGasConsumer(tt.args.meter, sigV2, tt.args.params)

		if tt.shouldErr {
			require.NotNil(t, err)
		} else {
			require.Nil(t, err)
			require.Equal(t, tt.gasConsumed, tt.args.meter.GasConsumed(), fmt.Sprintf("%d != %d", tt.gasConsumed, tt.args.meter.GasConsumed()))
		}
	}
}

func TestSigVerification(t *testing.T) {
	suite := SetupTestSuite(t, true)
	suite.txBankKeeper.EXPECT().DenomMetadata(gomock.Any(), gomock.Any()).Return(&banktypes.QueryDenomMetadataResponse{}, nil).AnyTimes()

	enabledSignModes := []signing.SignMode{signing.SignMode_SIGN_MODE_DIRECT, signing.SignMode_SIGN_MODE_TEXTUAL, signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON}
	// Since TEXTUAL is not enabled by default, we create a custom TxConfig
	// here which includes it.
	txConfigOpts := authtx.ConfigOptions{
		TextualCoinMetadataQueryFn: txmodule.NewGRPCCoinMetadataQueryFn(suite.clientCtx),
		EnabledSignModes:           enabledSignModes,
	}
	var err error
	suite.clientCtx.TxConfig, err = authtx.NewTxConfigWithOptions(
		codec.NewProtoCodec(suite.encCfg.InterfaceRegistry),
		txConfigOpts,
	)
	require.NoError(t, err)
	suite.txBuilder = suite.clientCtx.TxConfig.NewTxBuilder()

	// make block height non-zero to ensure account numbers part of signBytes
	suite.ctx = suite.ctx.WithBlockHeight(1)

	// keys and addresses
	priv1, _, addr1 := testdata.KeyTestPubAddr()
	priv2, _, addr2 := testdata.KeyTestPubAddr()
	priv3, _, addr3 := testdata.KeyTestPubAddr()

	addrs := []sdk.AccAddress{addr1, addr2, addr3}

	msgs := make([]sdk.Msg, len(addrs))
	accs := make([]sdk.AccountI, len(addrs))
	// set accounts and create msg for each address
	for i, addr := range addrs {
		acc := suite.accountKeeper.NewAccountWithAddress(suite.ctx, addr)
		require.NoError(t, acc.SetAccountNumber(uint64(i)+1000))
		suite.accountKeeper.SetAccount(suite.ctx, acc)
		msgs[i] = testdata.NewTestMsg(addr)
		accs[i] = acc
	}

	feeAmount := testdata.NewTestFeeAmount()
	gasLimit := testdata.NewTestGasLimit()

	spkd := ante.NewSetPubKeyDecorator(suite.accountKeeper)
	txConfigOpts = authtx.ConfigOptions{
		TextualCoinMetadataQueryFn: txmodule.NewBankKeeperCoinMetadataQueryFn(suite.txBankKeeper),
		EnabledSignModes:           enabledSignModes,
	}
	anteTxConfig, err := authtx.NewTxConfigWithOptions(
		codec.NewProtoCodec(suite.encCfg.InterfaceRegistry),
		txConfigOpts,
	)
	require.NoError(t, err)
	svd := ante.NewSigVerificationDecorator(suite.accountKeeper, anteTxConfig.SignModeHandler())
	antehandler := sdk.ChainAnteDecorators(spkd, svd)
	defaultSignMode, err := authsign.APISignModeToInternal(anteTxConfig.SignModeHandler().DefaultMode())
	require.NoError(t, err)

	type testCase struct {
		name        string
		privs       []cryptotypes.PrivKey
		accNums     []uint64
		accSeqs     []uint64
		invalidSigs bool // used for testing sigverify on RecheckTx
		recheck     bool
		sigverify   bool
		shouldErr   bool
	}
	validSigs := false
	testCases := []testCase{
		{"no signers", []cryptotypes.PrivKey{}, []uint64{}, []uint64{}, validSigs, false, true, true},
		{"not enough signers", []cryptotypes.PrivKey{priv1, priv2}, []uint64{accs[0].GetAccountNumber(), accs[1].GetAccountNumber()}, []uint64{0, 0}, validSigs, false, true, true},
		{"wrong order signers", []cryptotypes.PrivKey{priv3, priv2, priv1}, []uint64{accs[2].GetAccountNumber(), accs[1].GetAccountNumber(), accs[0].GetAccountNumber()}, []uint64{0, 0, 0}, validSigs, false, true, true},
		{"wrong accnums", []cryptotypes.PrivKey{priv1, priv2, priv3}, []uint64{7, 8, 9}, []uint64{0, 0, 0}, validSigs, false, true, true},
		{"wrong sequences", []cryptotypes.PrivKey{priv1, priv2, priv3}, []uint64{accs[0].GetAccountNumber(), accs[1].GetAccountNumber(), accs[2].GetAccountNumber()}, []uint64{3, 4, 5}, validSigs, false, true, true},
		{"valid tx", []cryptotypes.PrivKey{priv1, priv2, priv3}, []uint64{accs[0].GetAccountNumber(), accs[1].GetAccountNumber(), accs[2].GetAccountNumber()}, []uint64{0, 0, 0}, validSigs, false, true, false},
		{"sigverify tx with wrong order signers", []cryptotypes.PrivKey{priv3, priv2, priv1}, []uint64{accs[0].GetAccountNumber(), accs[1].GetAccountNumber(), accs[2].GetAccountNumber()}, []uint64{0, 0, 0}, validSigs, false, true, true},
		{"skip sigverify tx with wrong order signers", []cryptotypes.PrivKey{priv3, priv2, priv1}, []uint64{accs[0].GetAccountNumber(), accs[1].GetAccountNumber(), accs[2].GetAccountNumber()}, []uint64{0, 0, 0}, validSigs, false, false, false},
		{"no err on recheck", []cryptotypes.PrivKey{priv1, priv2, priv3}, []uint64{0, 0, 0}, []uint64{0, 0, 0}, !validSigs, true, true, false},
	}

	for i, tc := range testCases {
		for _, signMode := range enabledSignModes {
			t.Run(fmt.Sprintf("%s with %s", tc.name, signMode), func(t *testing.T) {
				suite.ctx = suite.ctx.WithIsReCheckTx(tc.recheck).WithIsSigverifyTx(tc.sigverify)
				suite.txBuilder = suite.clientCtx.TxConfig.NewTxBuilder() // Create new txBuilder for each test

				require.NoError(t, suite.txBuilder.SetMsgs(msgs...))
				suite.txBuilder.SetFeeAmount(feeAmount)
				suite.txBuilder.SetGasLimit(gasLimit)

				tx, err := suite.CreateTestTx(suite.ctx, tc.privs, tc.accNums, tc.accSeqs, suite.ctx.ChainID(), signMode)
				require.NoError(t, err)
				if tc.invalidSigs {
					txSigs, _ := tx.GetSignaturesV2()
					badSig, _ := tc.privs[0].Sign([]byte("unrelated message"))
					txSigs[0] = signing.SignatureV2{
						PubKey: tc.privs[0].PubKey(),
						Data: &signing.SingleSignatureData{
							SignMode:  defaultSignMode,
							Signature: badSig,
						},
						Sequence: tc.accSeqs[0],
					}
					suite.txBuilder.SetSignatures(txSigs...)
					tx = suite.txBuilder.GetTx()
				}

				txBytes, err := suite.clientCtx.TxConfig.TxEncoder()(tx)
				require.NoError(t, err)
				byteCtx := suite.ctx.WithTxBytes(txBytes)
				_, err = antehandler(byteCtx, tx, false)
				if tc.shouldErr {
					require.NotNil(t, err, "TestCase %d: %s did not error as expected", i, tc.name)
				} else {
					require.Nil(t, err, "TestCase %d: %s errored unexpectedly. Err: %v", i, tc.name, err)
				}
			})
		}
	}
}

func TestSigIntegration(t *testing.T) {
	// generate private keys
	privs := []cryptotypes.PrivKey{
		secp256k1.GenPrivKey(),
		secp256k1.GenPrivKey(),
		secp256k1.GenPrivKey(),
	}

	params := types.DefaultParams()
	initialSigCost := params.SigVerifyCostSecp256k1
	initialCost, err := runSigDecorators(t, params, false, privs...)
	require.Nil(t, err)

	params.SigVerifyCostSecp256k1 *= 2
	doubleCost, err := runSigDecorators(t, params, false, privs...)
	require.Nil(t, err)

	require.Equal(t, initialSigCost*uint64(len(privs)), doubleCost-initialCost)
}

func runSigDecorators(t *testing.T, params types.Params, _ bool, privs ...cryptotypes.PrivKey) (storetypes.Gas, error) {
	suite := SetupTestSuite(t, true)
	suite.txBuilder = suite.clientCtx.TxConfig.NewTxBuilder()

	// Make block-height non-zero to include accNum in SignBytes
	suite.ctx = suite.ctx.WithBlockHeight(1)
	err := suite.accountKeeper.Params.Set(suite.ctx, params)
	require.NoError(t, err)

	msgs := make([]sdk.Msg, len(privs))
	accNums := make([]uint64, len(privs))
	accSeqs := make([]uint64, len(privs))
	// set accounts and create msg for each address
	for i, priv := range privs {
		addr := sdk.AccAddress(priv.PubKey().Address())
		acc := suite.accountKeeper.NewAccountWithAddress(suite.ctx, addr)
		require.NoError(t, acc.SetAccountNumber(uint64(i)+1000))
		suite.accountKeeper.SetAccount(suite.ctx, acc)
		msgs[i] = testdata.NewTestMsg(addr)
		accNums[i] = acc.GetAccountNumber()
		accSeqs[i] = uint64(0)
	}
	require.NoError(t, suite.txBuilder.SetMsgs(msgs...))

	feeAmount := testdata.NewTestFeeAmount()
	gasLimit := testdata.NewTestGasLimit()
	suite.txBuilder.SetFeeAmount(feeAmount)
	suite.txBuilder.SetGasLimit(gasLimit)

	tx, err := suite.CreateTestTx(suite.ctx, privs, accNums, accSeqs, suite.ctx.ChainID(), signing.SignMode_SIGN_MODE_DIRECT)
	require.NoError(t, err)

	spkd := ante.NewSetPubKeyDecorator(suite.accountKeeper)
	svgc := ante.NewSigGasConsumeDecorator(suite.accountKeeper, ante.DefaultSigVerificationGasConsumer)
	svd := ante.NewSigVerificationDecorator(suite.accountKeeper, suite.clientCtx.TxConfig.SignModeHandler())
	antehandler := sdk.ChainAnteDecorators(spkd, svgc, svd)

	txBytes, err := suite.clientCtx.TxConfig.TxEncoder()(tx)
	require.NoError(t, err)
	suite.ctx = suite.ctx.WithTxBytes(txBytes)

	// Determine gas consumption of antehandler with default params
	before := suite.ctx.GasMeter().GasConsumed()
	ctx, err := antehandler(suite.ctx, tx, false)
	after := ctx.GasMeter().GasConsumed()

	return after - before, err
}

func TestIncrementSequenceDecorator(t *testing.T) {
	suite := SetupTestSuite(t, true)
	suite.txBuilder = suite.clientCtx.TxConfig.NewTxBuilder()

	priv, _, addr := testdata.KeyTestPubAddr()
	acc := suite.accountKeeper.NewAccountWithAddress(suite.ctx, addr)
	require.NoError(t, acc.SetAccountNumber(uint64(50)))
	suite.accountKeeper.SetAccount(suite.ctx, acc)

	msgs := []sdk.Msg{testdata.NewTestMsg(addr)}
	require.NoError(t, suite.txBuilder.SetMsgs(msgs...))
	privs := []cryptotypes.PrivKey{priv}
	accNums := []uint64{suite.accountKeeper.GetAccount(suite.ctx, addr).GetAccountNumber()}
	accSeqs := []uint64{suite.accountKeeper.GetAccount(suite.ctx, addr).GetSequence()}
	feeAmount := testdata.NewTestFeeAmount()
	gasLimit := testdata.NewTestGasLimit()
	suite.txBuilder.SetFeeAmount(feeAmount)
	suite.txBuilder.SetGasLimit(gasLimit)

	tx, err := suite.CreateTestTx(suite.ctx, privs, accNums, accSeqs, suite.ctx.ChainID(), signing.SignMode_SIGN_MODE_DIRECT)
	require.NoError(t, err)

	isd := ante.NewIncrementSequenceDecorator(suite.accountKeeper)
	antehandler := sdk.ChainAnteDecorators(isd)

	testCases := []struct {
		ctx         sdk.Context
		simulate    bool
		expectedSeq uint64
	}{
		{suite.ctx.WithIsReCheckTx(true), false, 1},
		{suite.ctx.WithIsCheckTx(true).WithIsReCheckTx(false), false, 2},
		{suite.ctx.WithIsReCheckTx(true), false, 3},
		{suite.ctx.WithIsReCheckTx(true), false, 4},
		{suite.ctx.WithIsReCheckTx(true), true, 5},
	}

	for i, tc := range testCases {
		_, err := antehandler(tc.ctx, tx, tc.simulate)
		require.NoError(t, err, "unexpected error; tc #%d, %v", i, tc)
		require.Equal(t, tc.expectedSeq, suite.accountKeeper.GetAccount(suite.ctx, addr).GetSequence())
	}
}

// TestSigVerificationWithSignatureVerifier checks that the decorator routes
// every signature through the configured verifier with the same inputs the
// default would get, and reports its outcome through the unchanged error paths.
func TestSigVerificationWithSignatureVerifier(t *testing.T) {
	suite := SetupTestSuite(t, true)
	suite.ctx = suite.ctx.WithBlockHeight(1).WithIsSigverifyTx(true)

	priv1, _, addr1 := testdata.KeyTestPubAddr()
	priv2, _, addr2 := testdata.KeyTestPubAddr()
	addrs := []sdk.AccAddress{addr1, addr2}
	privs := []cryptotypes.PrivKey{priv1, priv2}

	msgs := make([]sdk.Msg, len(addrs))
	accNums := make([]uint64, len(addrs))
	for i, addr := range addrs {
		acc := suite.accountKeeper.NewAccountWithAddress(suite.ctx, addr)
		require.NoError(t, acc.SetAccountNumber(uint64(i)+1000))
		suite.accountKeeper.SetAccount(suite.ctx, acc)
		msgs[i] = testdata.NewTestMsg(addr)
		accNums[i] = acc.GetAccountNumber()
	}
	accSeqs := []uint64{0, 0}

	signModeHandler := suite.clientCtx.TxConfig.SignModeHandler()

	// buildTx signs with signMode. corrupt replaces the first signature with one
	// over an unrelated message, which the default verifier rejects.
	buildTx := func(t *testing.T, signMode signing.SignMode, corrupt bool) authsign.Tx {
		t.Helper()
		suite.txBuilder = suite.clientCtx.TxConfig.NewTxBuilder()
		require.NoError(t, suite.txBuilder.SetMsgs(msgs...))
		suite.txBuilder.SetFeeAmount(testdata.NewTestFeeAmount())
		suite.txBuilder.SetGasLimit(testdata.NewTestGasLimit())

		tx, err := suite.CreateTestTx(suite.ctx, privs, accNums, accSeqs, suite.ctx.ChainID(), signMode)
		require.NoError(t, err)
		if !corrupt {
			return tx
		}

		txSigs, err := tx.GetSignaturesV2()
		require.NoError(t, err)
		badSig, err := privs[0].Sign([]byte("unrelated message"))
		require.NoError(t, err)
		txSigs[0] = signing.SignatureV2{
			PubKey:   privs[0].PubKey(),
			Data:     &signing.SingleSignatureData{SignMode: signMode, Signature: badSig},
			Sequence: accSeqs[0],
		}
		require.NoError(t, suite.txBuilder.SetSignatures(txSigs...))
		return suite.txBuilder.GetTx()
	}

	run := func(t *testing.T, verifier ante.SignatureVerifier, tx authsign.Tx) error {
		t.Helper()
		svd := ante.NewSigVerificationDecorator(suite.accountKeeper, signModeHandler, ante.WithSignatureVerifier(verifier))
		antehandler := sdk.ChainAnteDecorators(ante.NewSetPubKeyDecorator(suite.accountKeeper), svd)
		_, err := antehandler(suite.ctx, tx, false)
		return err
	}

	t.Run("verifier receives the default inputs and is called once per signature", func(t *testing.T) {
		type call struct {
			pubKey     cryptotypes.PubKey
			signerData txsigning.SignerData
			handler    *txsigning.HandlerMap
			txData     txsigning.TxData
		}
		var calls []call
		verifier := func(
			ctx context.Context,
			pubKey cryptotypes.PubKey,
			signerData txsigning.SignerData,
			sigData signing.SignatureData,
			handler *txsigning.HandlerMap,
			txData txsigning.TxData,
		) error {
			calls = append(calls, call{pubKey, signerData, handler, txData})
			return authsign.VerifySignature(ctx, pubKey, signerData, sigData, handler, txData)
		}

		require.NoError(t, run(t, verifier, buildTx(t, signing.SignMode_SIGN_MODE_DIRECT, false)))
		require.Len(t, calls, len(privs))

		for i, c := range calls {
			require.Equal(t, privs[i].PubKey(), c.pubKey, "signature %d", i)
			require.Equal(t, addrs[i].String(), c.signerData.Address, "signature %d", i)
			require.Equal(t, suite.ctx.ChainID(), c.signerData.ChainID, "signature %d", i)
			require.Equal(t, accNums[i], c.signerData.AccountNumber, "signature %d", i)
			require.Equal(t, accSeqs[i], c.signerData.Sequence, "signature %d", i)
			require.NotNil(t, c.signerData.PubKey, "signature %d", i)
			// The handler the decorator was constructed with, so a verifier can
			// produce sign bytes itself.
			require.Same(t, signModeHandler, c.handler, "signature %d", i)
			require.NotEmpty(t, c.txData.BodyBytes, "signature %d", i)
			require.NotEmpty(t, c.txData.AuthInfoBytes, "signature %d", i)
		}
	})

	t.Run("verifier accepting a signature the default would reject", func(t *testing.T) {
		// What a cache relies on: a hit returns nil without touching the curve.
		corrupt := buildTx(t, signing.SignMode_SIGN_MODE_DIRECT, true)

		require.Error(t, run(t, nil, corrupt), "a nil verifier must keep the default, which rejects this tx")

		accept := func(context.Context, cryptotypes.PubKey, txsigning.SignerData, signing.SignatureData, *txsigning.HandlerMap, txsigning.TxData) error {
			return nil
		}
		require.NoError(t, run(t, accept, corrupt))
	})

	t.Run("verifier error reaches the general error branch", func(t *testing.T) {
		reject := func(context.Context, cryptotypes.PubKey, txsigning.SignerData, signing.SignatureData, *txsigning.HandlerMap, txsigning.TxData) error {
			return fmt.Errorf("verifier rejected")
		}
		err := run(t, reject, buildTx(t, signing.SignMode_SIGN_MODE_DIRECT, false))
		require.ErrorIs(t, err, sdkerrors.ErrUnauthorized)
		require.ErrorContains(t, err, "signature verification failed; please verify account number")
		// The general branch quotes the verifier's error.
		require.ErrorContains(t, err, "verifier rejected")
	})

	t.Run("verifier error reaches the legacy amino error branch", func(t *testing.T) {
		reject := func(context.Context, cryptotypes.PubKey, txsigning.SignerData, signing.SignatureData, *txsigning.HandlerMap, txsigning.TxData) error {
			return fmt.Errorf("verifier rejected")
		}
		err := run(t, reject, buildTx(t, signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON, false))
		require.ErrorIs(t, err, sdkerrors.ErrUnauthorized)
		// The amino branch names the sequence instead of quoting the error.
		require.ErrorContains(t, err, "sequence")
		require.NotContains(t, err.Error(), "verifier rejected")
	})

	t.Run("verifier is not called on recheck", func(t *testing.T) {
		called := false
		verifier := func(context.Context, cryptotypes.PubKey, txsigning.SignerData, signing.SignatureData, *txsigning.HandlerMap, txsigning.TxData) error {
			called = true
			return nil
		}
		tx := buildTx(t, signing.SignMode_SIGN_MODE_DIRECT, false)

		recheckCtx := suite.ctx
		suite.ctx = suite.ctx.WithIsReCheckTx(true)
		defer func() { suite.ctx = recheckCtx }()

		require.NoError(t, run(t, verifier, tx))
		require.False(t, called, "the recheck skip must short-circuit before the verifier")
	})
}
