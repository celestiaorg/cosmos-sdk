package unknownproto

import (
	"bytes"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	gogotypes "github.com/cosmos/gogoproto/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/cosmos/cosmos-sdk/crypto/keys/multisig"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// Helpers that write protobuf fields by hand, so tests can build encodings
// that no encoder writes.

func bytesField(num protowire.Number, v []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, num, protowire.BytesType), v)
}

func varintField(num protowire.Number, v uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(nil, num, protowire.VarintType), v)
}

func fixed32Field(num protowire.Number, v uint32) []byte {
	return protowire.AppendFixed32(protowire.AppendTag(nil, num, protowire.Fixed32Type), v)
}

func cat(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}

func anyOf(typeURL string, value []byte) []byte {
	return cat(bytesField(1, []byte(typeURL)), bytesField(2, value))
}

// longVarint encodes v in one more byte than needed.
func longVarint(v uint64) []byte {
	b := protowire.AppendVarint(nil, v)
	b[len(b)-1] |= 0x80
	return append(b, 0x00)
}

func coinBytes(denom, amount string) []byte {
	return cat(bytesField(1, []byte(denom)), bytesField(2, []byte(amount)))
}

// checkStrict runs RejectAmbiguousEncoding with the gogoproto registry as the
// Any resolver.
func checkStrict(bz []byte, msg proto.Message) error {
	return RejectAmbiguousEncoding(bz, msg, DefaultAnyResolver{})
}

// TestRejectAmbiguousEncodingLevels checks each rule on a Customer1 placed at
// the top level, in a message field, in an Any, and in Anys and message fields
// nested in an Any.
func TestRejectAmbiguousEncodingLevels(t *testing.T) {
	levels := []struct {
		name string
		wrap func(leaf []byte) ([]byte, proto.Message)
	}{
		{"top level", func(leaf []byte) ([]byte, proto.Message) { return leaf, &testdata.Customer1{} }},
		{"message field", func(leaf []byte) ([]byte, proto.Message) {
			return bytesField(9, leaf), &testdata.Customer3{}
		}},
		{"Any", func(leaf []byte) ([]byte, proto.Message) {
			return bytesField(8, anyOf("/testpb.Customer1", leaf)), &testdata.TestVersion1{}
		}},
		{"message field in an Any", func(leaf []byte) ([]byte, proto.Message) {
			return bytesField(8, anyOf("/testpb.Customer3", bytesField(9, leaf))), &testdata.TestVersion1{}
		}},
		{"Any in an Any", func(leaf []byte) ([]byte, proto.Message) {
			inner := bytesField(8, anyOf("/testpb.Customer1", leaf))
			return bytesField(8, anyOf("/testpb.TestVersion1", inner)), &testdata.TestVersion1{}
		}},
	}

	// Customer1: id int32 = 1, name string = 2, subscription_fee float = 3,
	// payment string = 7.
	id, name := varintField(1, 5), bytesField(2, []byte("alice"))
	accepted := []struct {
		name string
		leaf []byte
	}{
		{"canonical", cat(id, name, fixed32Field(3, 7), bytesField(7, []byte("cash")))},
		{"empty message", nil},
		{"explicit default values", cat(varintField(1, 0), bytesField(2, nil), fixed32Field(3, 0))},
		{"negative int32 in ten bytes", cat(varintField(1, uint64(0xFFFF_FFFF_FFFF_FFFF)), name)},
		{"largest int32", varintField(1, 1<<31-1)},
		{"skipped field numbers", cat(id, bytesField(7, []byte("cash")))},
	}
	rejected := []struct {
		name string
		leaf []byte
	}{
		{"unknown field", cat(id, name, varintField(4, 1))},
		{"unknown non-critical field", cat(id, name, bytesField(1025, []byte("x")))},
		{"unknown field with the largest number", cat(id, varintField(protowire.MaxValidNumber, 1))},
		{"unknown group", cat(id, protowire.AppendTag(nil, 5, protowire.StartGroupType), protowire.AppendTag(nil, 5, protowire.EndGroupType))},
		{"known field as a group", cat(protowire.AppendTag(nil, 1, protowire.StartGroupType), protowire.AppendTag(nil, 1, protowire.EndGroupType))},
		{"field number zero", cat([]byte{0x00, 0x01}, id)},
		{"repeated scalar", cat(id, id)},
		{"repeated string", cat(id, name, name)},
		{"fields out of order", cat(name, id)},
		{"non-minimal varint", cat(protowire.AppendTag(nil, 1, protowire.VarintType), longVarint(5))},
		{"non-minimal zero varint", cat(protowire.AppendTag(nil, 1, protowire.VarintType), longVarint(0))},
		{"non-minimal tag", cat(longVarint(protowire.EncodeTag(1, protowire.VarintType)), []byte{5})},
		{"non-minimal length prefix", cat(protowire.AppendTag(nil, 2, protowire.BytesType), longVarint(5), []byte("alice"))},
		{"varint as fixed32", fixed32Field(1, 5)},
		{"singular varint in packed form", bytesField(1, []byte{5})},
		{"float as varint", varintField(3, 7)},
		{"string as varint", varintField(2, 7)},
		{"int32 above its range", varintField(1, 1<<31)},
		{"negative int32 in five bytes", varintField(1, 0xFFFF_FFFF)},
		{"invalid UTF-8", cat(id, bytesField(2, []byte{'a', 0xff}))},
		{"truncated field", cat(id, []byte{0x12, 0x05, 'a'})},
		{"trailing tag", cat(id, name, []byte{0x18})},
	}

	for _, lvl := range levels {
		for _, tc := range accepted {
			t.Run(lvl.name+"/accepts "+tc.name, func(t *testing.T) {
				bz, msg := lvl.wrap(tc.leaf)
				require.NoError(t, checkStrict(bz, msg))
			})
		}
		for _, tc := range rejected {
			t.Run(lvl.name+"/rejects "+tc.name, func(t *testing.T) {
				bz, msg := lvl.wrap(tc.leaf)
				require.Error(t, checkStrict(bz, msg))
			})
		}
	}
}

func TestRejectAmbiguousEncodingTypes(t *testing.T) {
	pubKey := bytesField(1, bytes.Repeat([]byte{2}, 33))
	pubKeyAny := anyOf("/cosmos.crypto.secp256k1.PubKey", pubKey)
	send := cat(bytesField(1, []byte("from")), bytesField(2, []byte("to")), bytesField(3, coinBytes("stake", "1")))
	sendAny := anyOf("/cosmos.bank.v1beta1.MsgSend", send)
	exec := func(msgAnys ...[]byte) []byte {
		b := bytesField(1, []byte("grantee"))
		for _, a := range msgAnys {
			b = append(b, bytesField(2, a)...)
		}
		return b
	}
	execAny := func(msgAnys ...[]byte) []byte { return anyOf("/cosmos.authz.v1beta1.MsgExec", exec(msgAnys...)) }
	single := bytesField(1, varintField(1, 1))
	multi := bytesField(2, cat(bytesField(1, varintField(1, 1)), bytesField(2, single)))
	timestamp := func(seconds, nanos uint64) []byte { return cat(varintField(1, seconds), varintField(2, nanos)) }
	grant := func(ts []byte) []byte {
		g := cat(bytesField(1, anyOf("/cosmos.authz.v1beta1.GenericAuthorization", bytesField(1, []byte("/x")))), bytesField(2, ts))
		return cat(bytesField(1, []byte("a")), bytesField(2, []byte("b")), bytesField(3, g))
	}

	testCases := []struct {
		name string
		msg  proto.Message
		bz   []byte
		ok   bool
	}{
		// Oneofs: ModeInfo{single = 1, multi = 2} and Customer3.payment.
		{"one oneof member", &tx.ModeInfo{}, single, true},
		{"two oneof members", &tx.ModeInfo{}, cat(single, multi), false},
		{"one oneof member twice", &tx.ModeInfo{}, cat(single, single), false},
		{"two string oneof members", &testdata.Customer3{}, cat(bytesField(7, []byte("a")), bytesField(8, []byte("b"))), false},
		{"oneof member nested in a oneof", &tx.ModeInfo{}, multi, true},

		// Enums: Customer2.city = 6.
		{"enum value not in the enum", &testdata.Customer2{}, varintField(6, 99), true},
		{"negative enum value", &testdata.Customer2{}, varintField(6, uint64(0xFFFF_FFFF_FFFF_FFFF)), true},
		{"enum value beyond 32 bits", &testdata.Customer2{}, varintField(6, 1<<32|1), false},
		{"enum mode beyond 32 bits", &tx.ModeInfo{}, bytesField(1, varintField(1, 1<<32|1)), false},

		// Bools: gov v1 MsgSubmitProposal.expedited = 7.
		{"bool one", &govv1.MsgSubmitProposal{}, varintField(7, 1), true},
		{"bool two", &govv1.MsgSubmitProposal{}, varintField(7, 2), false},

		// uint32: LegacyAminoPubKey.threshold = 1 and CompactBitArray.
		{"uint32 max", &multisig.LegacyAminoPubKey{}, varintField(1, 0xFFFF_FFFF), true},
		{"uint32 above its range", &multisig.LegacyAminoPubKey{}, varintField(1, 1<<32), false},
		{"uint32 above its range in a bit array", &cryptotypes.CompactBitArray{}, varintField(1, 1<<32), false},

		// int64 accepts every value.
		{"int64 max", &testdata.TestVersion1{}, varintField(1, 1<<63), true},

		// Packed repeated uint64: TestRepeatedUints.nums = 1.
		{"packed", &testdata.TestRepeatedUints{}, bytesField(1, []byte{1, 2, 0x80, 0x01}), true},
		{"empty packed", &testdata.TestRepeatedUints{}, bytesField(1, nil), true},
		{"unpacked", &testdata.TestRepeatedUints{}, cat(varintField(1, 1), varintField(1, 2)), false},
		{"packed in two runs", &testdata.TestRepeatedUints{}, cat(bytesField(1, []byte{1}), bytesField(1, []byte{2})), false},
		{"packed then unpacked", &testdata.TestRepeatedUints{}, cat(bytesField(1, []byte{1}), varintField(1, 2)), false},
		{"non-minimal packed element", &testdata.TestRepeatedUints{}, bytesField(1, []byte{1, 0x81, 0x00}), false},
		{"truncated packed element", &testdata.TestRepeatedUints{}, bytesField(1, []byte{1, 0x81}), false},

		// Unpacked repeated fields may repeat, in a row.
		{"repeated messages", &testdata.TestVersion1{}, cat(bytesField(4, nil), bytesField(4, varintField(1, 1))), true},
		{"repeated messages split by another field", &testdata.TestVersion1{}, cat(bytesField(4, nil), bytesField(5, nil), bytesField(4, nil)), false},

		// Maps (Nested3A.index = 5) cannot be resolved by the walk, as before.
		{"map entry", &testdata.Nested3A{}, bytesField(5, cat(varintField(1, 1), bytesField(2, varintField(1, 2)))), false},

		// math.Int and LegacyDec custom types.
		{"coin", &sdk.Coin{}, coinBytes("stake", "10"), true},
		{"zero coin", &sdk.Coin{}, coinBytes("stake", "0"), true},
		{"negative coin", &sdk.Coin{}, coinBytes("stake", "-10"), true},
		{"coin without amount", &sdk.Coin{}, bytesField(1, []byte("stake")), false},
		{"empty coin", &sdk.Coin{}, nil, false},
		{"empty amount", &sdk.Coin{}, coinBytes("stake", ""), false},
		{"amount with a plus sign", &sdk.Coin{}, coinBytes("stake", "+1"), false},
		{"hexadecimal amount", &sdk.Coin{}, coinBytes("stake", "0x1"), false},
		{"amount with an underscore", &sdk.Coin{}, coinBytes("stake", "1_0"), false},
		{"amount with a leading zero", &sdk.Coin{}, coinBytes("stake", "01"), false},
		{"negative zero amount", &sdk.Coin{}, coinBytes("stake", "-0"), false},
		{"lone minus amount", &sdk.Coin{}, coinBytes("stake", "-"), false},
		{"amount with a space", &sdk.Coin{}, coinBytes("stake", " 1"), false},
		{"amount with a point", &sdk.Coin{}, coinBytes("stake", "1.0"), false},
		{"dec coin", &sdk.DecCoin{}, coinBytes("stake", "1500000000000000000"), true},
		{"dec coin with a point", &sdk.DecCoin{}, coinBytes("stake", "1.5"), false},
		{"nullable decimals left out", &stakingtypes.MsgEditValidator{}, bytesField(2, []byte("val")), true},
		{"nullable decimal not canonical", &stakingtypes.MsgEditValidator{}, bytesField(3, []byte("01")), false},
		{"non-nullable coin left out", &stakingtypes.MsgDelegate{}, bytesField(1, []byte("del")), true},
		{"empty non-nullable coin", &stakingtypes.MsgDelegate{}, bytesField(3, nil), false},
		{"second coin without amount", &banktypes.MsgSend{}, cat(bytesField(3, coinBytes("a", "1")), bytesField(3, bytesField(1, []byte("b")))), false},

		// Anys, in TestVersion1.g = 8.
		{"Any", &testdata.TestVersion1{}, bytesField(8, sendAny), true},
		{"Any without value", &testdata.TestVersion1{}, bytesField(8, bytesField(1, []byte("/testpb.Customer1"))), true},
		{"Any value before type URL", &testdata.TestVersion1{}, bytesField(8, cat(bytesField(2, send), bytesField(1, []byte("/cosmos.bank.v1beta1.MsgSend")))), false},
		{"Any with two type URLs", &testdata.TestVersion1{}, bytesField(8, cat(bytesField(1, []byte("/cosmos.bank.v1beta1.MsgSend")), sendAny)), false},
		{"Any with two values", &testdata.TestVersion1{}, bytesField(8, cat(sendAny, bytesField(2, send))), false},
		{"Any with an unknown field", &testdata.TestVersion1{}, bytesField(8, cat(sendAny, varintField(3, 1))), false},
		{"Any with an unregistered type", &testdata.TestVersion1{}, bytesField(8, anyOf("/cosmos.bank.v1beta1.Unknown", nil)), false},
		{"Any without type URL", &testdata.TestVersion1{}, bytesField(8, bytesField(2, send)), false},
		{"Any with invalid UTF-8 type URL", &testdata.TestVersion1{}, bytesField(8, anyOf("/cosmos\xff", nil)), false},

		// Nested MsgExec and multisig public keys.
		{"MsgExec in MsgExec", &testdata.TestVersion1{}, bytesField(8, execAny(execAny(sendAny))), true},
		{"repeated field in MsgExec in MsgExec", &testdata.TestVersion1{}, bytesField(8, execAny(execAny(anyOf("/cosmos.bank.v1beta1.MsgSend", cat(send, bytesField(2, []byte("to"))))))), false},
		{"bad decimal in MsgExec in MsgExec", &testdata.TestVersion1{}, bytesField(8, execAny(execAny(anyOf("/cosmos.bank.v1beta1.MsgSend", cat(bytesField(1, []byte("a")), bytesField(3, coinBytes("s", "01"))))))), false},
		{"multisig in multisig", &multisig.LegacyAminoPubKey{}, cat(varintField(1, 1), bytesField(2, anyOf("/cosmos.crypto.multisig.LegacyAminoPubKey", cat(varintField(1, 1), bytesField(2, pubKeyAny))))), true},
		{"repeated key in multisig in multisig", &multisig.LegacyAminoPubKey{}, cat(varintField(1, 1), bytesField(2, anyOf("/cosmos.crypto.multisig.LegacyAminoPubKey", cat(varintField(1, 1), bytesField(2, anyOf("/cosmos.crypto.secp256k1.PubKey", cat(pubKey, pubKey))))))), false},
		{"bytes field with invalid UTF-8", &testdata.TestVersion1{}, bytesField(8, anyOf("/cosmos.crypto.secp256k1.PubKey", bytesField(1, []byte{0xff, 0xfe}))), true},

		// Timestamp and Duration: seconds int64 = 1, nanos int32 = 2.
		{"timestamp", &authz.MsgGrant{}, grant(timestamp(1_700_000_000, 5)), true},
		{"explicit zero timestamp", &authz.MsgGrant{}, grant(timestamp(0, 0)), true},
		{"timestamp nanos beyond int32", &authz.MsgGrant{}, grant(timestamp(1, 1<<32)), false},
		{"timestamp fields out of order", &authz.MsgGrant{}, grant(cat(varintField(2, 1), varintField(1, 1))), false},
		{"duration", &gogotypes.Duration{}, timestamp(3, 4), true},
		{"duration nanos beyond int32", &gogotypes.Duration{}, timestamp(3, 1<<31), false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkStrict(tc.bz, tc.msg)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// TestRejectAmbiguousEncodingDepth checks the nesting limit: 100 messages,
// counting an Any and the message in it as two.
func TestRejectAmbiguousEncodingDepth(t *testing.T) {
	// TestVersion1.a = 2 nests a message; TestVersion1.g = 8 nests an Any.
	nestFields := func(n int) []byte {
		var b []byte
		for range n {
			b = bytesField(2, b)
		}
		return b
	}
	nestAnys := func(n int) []byte {
		var b []byte
		for range n {
			b = bytesField(8, anyOf("/testpb.TestVersion1", b))
		}
		return b
	}
	require.NoError(t, checkStrict(nestFields(99), &testdata.TestVersion1{}))
	require.Error(t, checkStrict(nestFields(100), &testdata.TestVersion1{}))
	require.NoError(t, checkStrict(nestAnys(49), &testdata.TestVersion1{}))
	require.Error(t, checkStrict(nestAnys(50), &testdata.TestVersion1{}))

	// The default walk keeps its own, much higher, limit.
	_, err := RejectUnknownFields(nestFields(500), &testdata.TestVersion1{}, false, DefaultAnyResolver{})
	require.NoError(t, err)
	_, err = RejectUnknownFields(nestAnys(500), &testdata.TestVersion1{}, false, DefaultAnyResolver{})
	require.NoError(t, err)
}

func TestCheckVarints(t *testing.T) {
	type T = descriptorpb.FieldDescriptorProto_Type
	testCases := []struct {
		name   string
		typ    T
		packed bool
		b      []byte
		ok     bool
	}{
		{"sint32 max zigzag", descriptorpb.FieldDescriptorProto_TYPE_SINT32, false, protowire.AppendVarint(nil, 0xFFFF_FFFF), true},
		{"sint32 beyond 32 bits", descriptorpb.FieldDescriptorProto_TYPE_SINT32, false, protowire.AppendVarint(nil, 1<<32), false},
		{"sint64 max", descriptorpb.FieldDescriptorProto_TYPE_SINT64, false, protowire.AppendVarint(nil, ^uint64(0)), true},
		{"uint64 max", descriptorpb.FieldDescriptorProto_TYPE_UINT64, false, protowire.AppendVarint(nil, ^uint64(0)), true},
		{"int32 min", descriptorpb.FieldDescriptorProto_TYPE_INT32, false, protowire.AppendVarint(nil, uint64(1<<64-1<<31)), true},
		{"int32 below min", descriptorpb.FieldDescriptorProto_TYPE_INT32, false, protowire.AppendVarint(nil, uint64(1<<64-1<<31-1)), false},
		{"bool zero", descriptorpb.FieldDescriptorProto_TYPE_BOOL, false, []byte{0}, true},
		{"bool two", descriptorpb.FieldDescriptorProto_TYPE_BOOL, false, []byte{2}, false},
		{"overflowing varint", descriptorpb.FieldDescriptorProto_TYPE_UINT64, false, bytes.Repeat([]byte{0xff}, 10), false},
		{"packed fixed32", descriptorpb.FieldDescriptorProto_TYPE_FIXED32, true, make([]byte, 8), true},
		{"packed fixed32 cut short", descriptorpb.FieldDescriptorProto_TYPE_SFIXED32, true, make([]byte, 6), false},
		{"packed fixed64", descriptorpb.FieldDescriptorProto_TYPE_DOUBLE, true, make([]byte, 16), true},
		{"packed fixed64 cut short", descriptorpb.FieldDescriptorProto_TYPE_FIXED64, true, make([]byte, 12), false},
		{"packed bools", descriptorpb.FieldDescriptorProto_TYPE_BOOL, true, []byte{0, 1, 1}, true},
		{"packed bool two", descriptorpb.FieldDescriptorProto_TYPE_BOOL, true, []byte{0, 2}, false},
		{"packed sint32 beyond 32 bits", descriptorpb.FieldDescriptorProto_TYPE_SINT32, true, cat([]byte{1}, protowire.AppendVarint(nil, 1<<32)), false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkVarints(tc.b, tc.typ, tc.packed)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// FuzzRejectAmbiguousEncoding checks that the strict walk returns on any
// bytes and accepts only what the default strict walk accepts.
func FuzzRejectAmbiguousEncoding(f *testing.F) {
	seeds := [][]byte{
		cat(varintField(1, 5), bytesField(2, []byte("alice")), fixed32Field(3, 7)),
		bytesField(8, anyOf("/testpb.TestVersion1", bytesField(8, anyOf("/testpb.Customer1", varintField(1, 1))))),
		cat(varintField(1, 1), bytesField(4, varintField(1, 2)), bytesField(7, varintField(1, 3))),
		bytesField(8, anyOf("/cosmos.authz.v1beta1.MsgExec", cat(bytesField(1, []byte("g")), bytesField(2, anyOf("/cosmos.bank.v1beta1.MsgSend", bytesField(3, coinBytes("s", "1"))))))),
		bytesField(8, anyOf("/cosmos.crypto.multisig.LegacyAminoPubKey", cat(varintField(1, 1), bytesField(2, anyOf("/cosmos.crypto.secp256k1.PubKey", bytesField(1, []byte{2, 3})))))),
		bytesField(8, anyOf("/testpb.TestRepeatedUints", bytesField(1, []byte{1, 2, 3}))),
		bytesField(8, anyOf("/testpb.Customer3", cat(bytesField(7, []byte("a")), bytesField(9, varintField(1, 1))))),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, bz []byte) {
		if err := checkStrict(bz, &testdata.TestVersion1{}); err != nil {
			return
		}
		require.NoError(t, RejectUnknownFieldsStrict(bz, &testdata.TestVersion1{}, DefaultAnyResolver{}))
	})
}
