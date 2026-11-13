package tx_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	sdkmath "cosmossdk.io/math"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/multisig"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	"github.com/cosmos/cosmos-sdk/x/auth/vesting"
	"github.com/cosmos/cosmos-sdk/x/authz"
	authzmodule "github.com/cosmos/cosmos-sdk/x/authz/module"
	"github.com/cosmos/cosmos-sdk/x/bank"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/cosmos-sdk/x/consensus"
	"github.com/cosmos/cosmos-sdk/x/crisis"
	distribution "github.com/cosmos/cosmos-sdk/x/distribution"
	"github.com/cosmos/cosmos-sdk/x/gov"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	govv1beta1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1beta1"
	"github.com/cosmos/cosmos-sdk/x/group"
	groupmodule "github.com/cosmos/cosmos-sdk/x/group/module"
	"github.com/cosmos/cosmos-sdk/x/mint"
	"github.com/cosmos/cosmos-sdk/x/params"
	"github.com/cosmos/cosmos-sdk/x/slashing"
	"github.com/cosmos/cosmos-sdk/x/staking"
)

func strictTestConfig() moduletestutil.TestEncodingConfig {
	return moduletestutil.MakeTestEncodingConfig(
		auth.AppModuleBasic{}, bank.AppModuleBasic{}, authzmodule.AppModuleBasic{}, gov.NewAppModuleBasic(nil),
		staking.AppModuleBasic{}, distribution.AppModuleBasic{}, slashing.AppModuleBasic{}, mint.AppModuleBasic{},
		crisis.AppModuleBasic{}, params.AppModuleBasic{}, consensus.AppModuleBasic{}, groupmodule.AppModuleBasic{},
		vesting.AppModuleBasic{},
	)
}

var (
	strictFrom = sdk.AccAddress(bytes.Repeat([]byte{1}, 20))
	strictTo   = sdk.AccAddress(bytes.Repeat([]byte{2}, 20))
)

func protoField(num protowire.Number, v []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, num, protowire.BytesType), v)
}

func varintProtoField(num protowire.Number, v uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(nil, num, protowire.VarintType), v)
}

func join(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func mustMarshalProto(t *testing.T, m proto.Message) []byte {
	t.Helper()
	bz, err := proto.Marshal(m)
	require.NoError(t, err)
	return bz
}

func anyField(num protowire.Number, typeURL string, value []byte) []byte {
	return protoField(num, join(protoField(1, []byte(typeURL)), protoField(2, value)))
}

// strictTxParts are canonical pieces of a tx that the cases below change.
type strictTxParts struct {
	send, body, authInfo, signerInfo, fee []byte
}

func newStrictTxParts(t *testing.T) strictTxParts {
	t.Helper()
	var p strictTxParts
	p.send = mustMarshalProto(t, banktypes.NewMsgSend(strictFrom, strictTo, sdk.NewCoins(sdk.NewInt64Coin("stake", 1))))
	p.body = join(anyField(1, "/cosmos.bank.v1beta1.MsgSend", p.send), protoField(2, []byte("memo")))
	pk, err := codectypes.NewAnyWithValue(secp256k1.GenPrivKeyFromSecret([]byte("strict")).PubKey())
	require.NoError(t, err)
	p.signerInfo = join(anyField(1, pk.TypeUrl, pk.Value), protoField(2, protoField(1, varintProtoField(1, 1))), varintProtoField(3, 1))
	p.fee = join(protoField(1, join(protoField(1, []byte("stake")), protoField(2, []byte("10")))), varintProtoField(2, 100_000))
	p.authInfo = join(protoField(1, p.signerInfo), protoField(2, p.fee))
	return p
}

func strictTxRaw(body, authInfo []byte) []byte {
	return join(protoField(1, body), protoField(2, authInfo), protoField(3, bytes.Repeat([]byte{7}, 64)))
}

// TestStrictTxDecoder checks the strict decoder on each part of a tx, and that
// DefaultTxDecoder still accepts what only the strict decoder rejects.
func TestStrictTxDecoder(t *testing.T) {
	cfg := strictTestConfig()
	strict, def := authtx.StrictTxDecoder(cfg.Codec), authtx.DefaultTxDecoder(cfg.Codec)
	p := newStrictTxParts(t)
	execAny := func(num protowire.Number, inner []byte) []byte {
		return anyField(num, "/cosmos.authz.v1beta1.MsgExec", join(protoField(1, []byte(strictTo.String())), inner))
	}
	sendAny := func(num protowire.Number, send []byte) []byte { return anyField(num, "/cosmos.bank.v1beta1.MsgSend", send) }
	longOne := []byte{0x81, 0x00}

	testCases := []struct {
		name          string
		raw           []byte
		strictOK      bool
		defaultOK     bool
		defaultErrMsg string
	}{
		{"canonical", strictTxRaw(p.body, p.authInfo), true, true, ""},
		{"no signer", strictTxRaw(p.body, protoField(2, p.fee)), true, true, ""},
		{"explicit default timeout height", strictTxRaw(join(p.body, varintProtoField(3, 0)), p.authInfo), true, true, ""},
		{"message in MsgExec in MsgExec", strictTxRaw(execAny(1, execAny(2, sendAny(2, p.send))), p.authInfo), true, true, ""},

		// TxRaw.
		{"TxRaw unknown field", join(strictTxRaw(p.body, p.authInfo), protoField(4, nil)), false, false, ""},
		{"TxRaw body twice", join(protoField(1, p.body), protoField(1, p.body), protoField(2, p.authInfo)), false, true, ""},

		// TxBody.
		{"TxBody non-critical unknown field", strictTxRaw(join(p.body, protoField(1025, []byte("x"))), p.authInfo), false, true, ""},
		{"TxBody memo twice", strictTxRaw(join(p.body, protoField(2, []byte("again"))), p.authInfo), false, true, ""},
		{"TxBody fields out of order", strictTxRaw(join(protoField(2, []byte("memo")), sendAny(1, p.send)), p.authInfo), false, true, ""},
		{"TxBody non-minimal varint", strictTxRaw(join(p.body, protowire.AppendTag(nil, 3, protowire.VarintType), longOne), p.authInfo), false, true, ""},
		{"TxBody invalid UTF-8 memo", strictTxRaw(join(sendAny(1, p.send), protoField(2, []byte{0xff})), p.authInfo), false, true, ""},
		{"TxBody unregistered extension option", strictTxRaw(join(p.body, anyField(1023, "/cosmos.bank.v1beta1.MsgSend", p.send)), p.authInfo), false, false, ""},

		// AuthInfo.
		{"AuthInfo fee twice", strictTxRaw(p.body, join(p.authInfo, protoField(2, p.fee))), false, true, ""},
		{"AuthInfo non-canonical fee amount", strictTxRaw(p.body, join(protoField(1, p.signerInfo), protoField(2, join(protoField(1, join(protoField(1, []byte("stake")), protoField(2, []byte("010")))), varintProtoField(2, 1))))), false, true, ""},
		{"AuthInfo two oneof members in mode info", strictTxRaw(p.body, join(protoField(1, join(p.signerInfo[:len(p.signerInfo)-2], protoField(2, protoField(2, nil)))), protoField(2, p.fee))), false, true, ""},

		// Messages.
		{"message field twice", strictTxRaw(sendAny(1, join(p.send, protoField(1, []byte("x")))), p.authInfo), false, true, ""},
		{"message coin without amount", strictTxRaw(sendAny(1, join(protoField(1, []byte("a")), protoField(2, []byte("b")), protoField(3, protoField(1, []byte("stake"))))), p.authInfo), false, true, ""},
		{"message in MsgExec in MsgExec with unknown field", strictTxRaw(execAny(1, execAny(2, sendAny(2, join(p.send, varintProtoField(1025, 1))))), p.authInfo), false, true, ""},
		{"message in MsgExec in MsgExec out of order", strictTxRaw(execAny(1, execAny(2, sendAny(2, join(protoField(2, []byte("b")), protoField(1, []byte("a")))))), p.authInfo), false, true, ""},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := strict(tc.raw)
			if tc.strictOK {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, sdkerrors.ErrTxDecode)
			}
			_, err = def(tc.raw)
			if tc.defaultOK {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// TestStrictTxDecoderAcceptsEncoderOutput checks that a tx the SDK encodes,
// holding any registered message with every field set (Anys and oneofs
// included where a value fits), decodes under the strict decoder whenever it
// decodes under DefaultTxDecoder, to the same tx.
func TestStrictTxDecoderAcceptsEncoderOutput(t *testing.T) {
	cfg := strictTestConfig()
	strict, def := authtx.StrictTxDecoder(cfg.Codec), authtx.DefaultTxDecoder(cfg.Codec)
	anys := candidateAnys(t)
	checked, withAny := 0, 0
	for _, url := range cfg.InterfaceRegistry.ListImplementations(sdk.MsgInterfaceProtoName) {
		ok, usedAny := false, false
		// Try each Any value until DefaultTxDecoder accepts the tx.
		for _, a := range anys {
			msg, err := cfg.InterfaceRegistry.Resolve(url)
			require.NoError(t, err)
			filled := fillValue(reflect.ValueOf(msg).Elem(), a, 0)
			raw := encodeStrictTestTx(t, cfg, msg)
			if raw == nil {
				break
			}
			defTx, defErr := def(raw)
			strictTx, strictErr := strict(raw)
			if defErr != nil {
				require.Error(t, strictErr, url)
				if filled {
					continue
				}
				break
			}
			require.NoError(t, strictErr, url)
			require.Equal(t, mustMarshalProto(t, defTx.(interface{ GetProtoTx() *txtypes.Tx }).GetProtoTx()),
				mustMarshalProto(t, strictTx.(interface{ GetProtoTx() *txtypes.Tx }).GetProtoTx()), url)
			ok, usedAny = true, filled
			break
		}
		if ok {
			checked++
			if usedAny {
				withAny++
			}
		} else {
			t.Logf("%s: DefaultTxDecoder rejects every filled form", url)
		}
	}
	t.Logf("checked %d message types, %d with an Any filled", checked, withAny)
	require.Greater(t, checked, 50)
	require.Greater(t, withAny, 5)
}

func encodeStrictTestTx(t *testing.T, cfg moduletestutil.TestEncodingConfig, msg sdk.Msg) []byte {
	t.Helper()
	builder := cfg.TxConfig.NewTxBuilder()
	if err := builder.SetMsgs(msg); err != nil {
		return nil
	}
	builder.SetMemo("memo")
	builder.SetGasLimit(100_000)
	builder.SetFeeAmount(sdk.NewCoins(sdk.NewInt64Coin("stake", 1)))
	pk := secp256k1.GenPrivKeyFromSecret([]byte("strict")).PubKey()
	require.NoError(t, builder.SetSignatures(signingtypes.SignatureV2{
		PubKey:   pk,
		Data:     &signingtypes.SingleSignatureData{SignMode: signingtypes.SignMode_SIGN_MODE_DIRECT, Signature: make([]byte, 64)},
		Sequence: 1,
	}))
	raw, err := cfg.TxConfig.TxEncoder()(builder.GetTx())
	require.NoError(t, err)
	return raw
}

// candidateAnys returns values for Any fields: a message, a public key, a
// multisig key, an authorization and a proposal content.
func candidateAnys(t *testing.T) []*codectypes.Any {
	t.Helper()
	k1 := secp256k1.GenPrivKeyFromSecret([]byte("a")).PubKey()
	k2 := secp256k1.GenPrivKeyFromSecret([]byte("b")).PubKey()
	values := []proto.Message{
		banktypes.NewMsgSend(strictFrom, strictTo, sdk.NewCoins(sdk.NewInt64Coin("stake", 1))),
		k1,
		multisig.NewLegacyAminoPubKey(1, []cryptotypes.PubKey{k1, k2}),
		authz.NewGenericAuthorization("/cosmos.bank.v1beta1.MsgSend"),
		&govv1beta1.TextProposal{Title: "title", Description: "description"},
		&govv1.MsgVote{ProposalId: 1, Voter: strictFrom.String(), Option: govv1.OptionYes},
		group.NewThresholdDecisionPolicy("1", time.Hour, 0),
	}
	out := make([]*codectypes.Any, len(values))
	for i, v := range values {
		a, err := codectypes.NewAnyWithValue(v)
		require.NoError(t, err)
		out[i] = a
	}
	return out
}

var (
	strictIntType  = reflect.TypeOf(sdkmath.Int{})
	strictDecType  = reflect.TypeOf(sdkmath.LegacyDec{})
	strictTimeType = reflect.TypeOf(time.Time{})
	strictAnyType  = reflect.TypeOf(&codectypes.Any{})
)

// fillValue sets every exported field of v to a non-default value, Any fields
// to anyValue and oneofs to their first member. It reports whether it set an
// Any.
func fillValue(v reflect.Value, anyValue *codectypes.Any, depth int) bool {
	switch v.Type() {
	case strictIntType:
		v.Set(reflect.ValueOf(sdkmath.NewInt(7)))
		return false
	case strictDecType:
		v.Set(reflect.ValueOf(sdkmath.LegacyNewDecWithPrec(7, 1)))
		return false
	case strictTimeType:
		v.Set(reflect.ValueOf(time.Unix(7, 8).UTC()))
		return false
	case strictAnyType:
		if depth > 3 {
			return false
		}
		a := *anyValue
		v.Set(reflect.ValueOf(&a))
		return true
	}
	filled := false
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Array:
		for i := range v.Len() {
			filled = fillValue(v.Index(i), anyValue, depth) || filled
		}
	case reflect.Slice:
		if depth > 3 {
			return false
		}
		s := reflect.MakeSlice(v.Type(), 2, 2)
		for i := range 2 {
			filled = fillValue(s.Index(i), anyValue, depth+1) || filled
		}
		v.Set(s)
	case reflect.Pointer:
		if depth > 3 {
			return false
		}
		p := reflect.New(v.Type().Elem())
		filled = fillValue(p.Elem(), anyValue, depth+1)
		v.Set(p)
	case reflect.Struct:
		filled = fillStruct(v, anyValue, depth)
	}
	return filled
}

func fillStruct(v reflect.Value, anyValue *codectypes.Any, depth int) bool {
	filled := false
	var wrappers []any
	if w, ok := v.Addr().Interface().(interface{ XXX_OneofWrappers() []any }); ok {
		wrappers = w.XXX_OneofWrappers()
	}
	for i := range v.NumField() {
		sf := v.Type().Field(i)
		if !sf.IsExported() || strings.HasPrefix(sf.Name, "XXX_") {
			continue
		}
		if sf.Type.Kind() != reflect.Interface {
			filled = fillValue(v.Field(i), anyValue, depth) || filled
			continue
		}
		// A oneof: set its first member.
		for _, w := range wrappers {
			wv := reflect.New(reflect.TypeOf(w).Elem())
			if wv.Type().Implements(sf.Type) && depth <= 3 {
				filled = fillValue(wv.Elem(), anyValue, depth+1) || filled
				v.Field(i).Set(wv)
				break
			}
		}
	}
	return filled
}

// FuzzStrictTxDecoder checks that the strict decoder returns on any bytes, and
// that every tx it accepts DefaultTxDecoder decodes to the same tx.
func FuzzStrictTxDecoder(f *testing.F) {
	cfg := strictTestConfig()
	strict, def := authtx.StrictTxDecoder(cfg.Codec), authtx.DefaultTxDecoder(cfg.Codec)
	exec := authz.NewMsgExec(strictTo, []sdk.Msg{banktypes.NewMsgSend(strictFrom, strictTo, sdk.NewCoins(sdk.NewInt64Coin("stake", 1)))})
	k1 := secp256k1.GenPrivKeyFromSecret([]byte("a")).PubKey()
	for _, msgs := range [][]sdk.Msg{
		{banktypes.NewMsgSend(strictFrom, strictTo, sdk.NewCoins(sdk.NewInt64Coin("stake", 1)))},
		{&exec},
		{&govv1.MsgSubmitProposal{Messages: exec.Msgs, Proposer: strictFrom.String(), Expedited: true}},
	} {
		builder := cfg.TxConfig.NewTxBuilder()
		if err := builder.SetMsgs(msgs...); err != nil {
			f.Fatal(err)
		}
		builder.SetMemo("memo")
		builder.SetFeeAmount(sdk.NewCoins(sdk.NewInt64Coin("stake", 1)))
		if err := builder.SetSignatures(signingtypes.SignatureV2{
			PubKey: multisig.NewLegacyAminoPubKey(1, []cryptotypes.PubKey{k1}),
			Data:   &signingtypes.SingleSignatureData{SignMode: signingtypes.SignMode_SIGN_MODE_DIRECT},
		}); err != nil {
			f.Fatal(err)
		}
		raw, err := cfg.TxConfig.TxEncoder()(builder.GetTx())
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		strictTx, err := strict(raw)
		if err != nil {
			return
		}
		defTx, err := def(raw)
		require.NoError(t, err)
		require.Equal(t, mustMarshalProto(t, defTx.(interface{ GetProtoTx() *txtypes.Tx }).GetProtoTx()),
			mustMarshalProto(t, strictTx.(interface{ GetProtoTx() *txtypes.Tx }).GetProtoTx()))
	})
}
