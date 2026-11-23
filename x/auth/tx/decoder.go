package tx

import (
	"fmt"
	mathbits "math/bits"

	"github.com/cosmos/gogoproto/jsonpb"
	gogoproto "github.com/cosmos/gogoproto/proto"
	"google.golang.org/protobuf/encoding/protowire"

	errorsmod "cosmossdk.io/errors"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/unknownproto"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/tx"
)

// DefaultTxDecoder returns a default protobuf TxDecoder using the provided Marshaler.
func DefaultTxDecoder(cdc codec.Codec) sdk.TxDecoder {
	return newTxDecoder(cdc, false)
}

// StrictTxDecoder is DefaultTxDecoder that also rejects encodings protobuf
// decoders can read differently; see unknownproto.RejectAmbiguousEncoding.
func StrictTxDecoder(cdc codec.Codec) sdk.TxDecoder {
	return newTxDecoder(cdc, true)
}

func newTxDecoder(cdc codec.Codec, strict bool) sdk.TxDecoder {
	rejectUnknown := func(bz []byte, msg gogoproto.Message, allowNonCriticals bool) (bool, error) {
		if strict {
			return false, unknownproto.RejectAmbiguousEncoding(bz, msg, cdc.InterfaceRegistry())
		}
		return unknownproto.RejectUnknownFields(bz, msg, allowNonCriticals, cdc.InterfaceRegistry())
	}
	return func(txBytes []byte) (sdk.Tx, error) {
		// Make sure txBytes follow ADR-027.
		err := rejectNonADR027TxRaw(txBytes)
		if err != nil {
			return nil, errorsmod.Wrap(sdkerrors.ErrTxDecode, err.Error())
		}

		var raw tx.TxRaw

		// reject all unknown proto fields in the root TxRaw
		_, err = rejectUnknown(txBytes, &raw, false)
		if err != nil {
			return nil, errorsmod.Wrap(sdkerrors.ErrTxDecode, err.Error())
		}

		err = cdc.Unmarshal(txBytes, &raw)
		if err != nil {
			return nil, err
		}

		var body tx.TxBody

		// allow non-critical unknown fields in TxBody
		txBodyHasUnknownNonCriticals, err := rejectUnknown(raw.BodyBytes, &body, true)
		if err != nil {
			return nil, errorsmod.Wrap(sdkerrors.ErrTxDecode, err.Error())
		}

		err = cdc.Unmarshal(raw.BodyBytes, &body)
		if err != nil {
			return nil, errorsmod.Wrap(sdkerrors.ErrTxDecode, err.Error())
		}

		var authInfo tx.AuthInfo

		// reject all unknown proto fields in AuthInfo
		_, err = rejectUnknown(raw.AuthInfoBytes, &authInfo, false)
		if err != nil {
			return nil, errorsmod.Wrap(sdkerrors.ErrTxDecode, err.Error())
		}

		err = cdc.Unmarshal(raw.AuthInfoBytes, &authInfo)
		if err != nil {
			return nil, errorsmod.Wrap(sdkerrors.ErrTxDecode, err.Error())
		}

		if strict {
			for i, signer := range authInfo.SignerInfos {
				if i >= len(raw.Signatures) {
					break
				}
				if err := rejectAmbiguousMultiSignature(signer.ModeInfo, raw.Signatures[i], cdc.InterfaceRegistry()); err != nil {
					return nil, errorsmod.Wrap(sdkerrors.ErrTxDecode, err.Error())
				}
			}
		}

		theTx := &tx.Tx{
			Body:       &body,
			AuthInfo:   &authInfo,
			Signatures: raw.Signatures,
		}

		return &wrapper{
			tx:                           theTx,
			bodyBz:                       raw.BodyBytes,
			authInfoBz:                   raw.AuthInfoBytes,
			txBodyHasUnknownNonCriticals: txBodyHasUnknownNonCriticals,
			cdc:                          cdc,
		}, nil
	}
}

// rejectAmbiguousMultiSignature applies the strict walk to sig when mode is a
// multisig mode, at every level of nesting. With SIGN_MODE_LEGACY_AMINO_JSON no
// signature covers these bytes or the mode info, so the bit array must be
// canonical and sig must hold exactly one signature per selected key.
func rejectAmbiguousMultiSignature(mode *tx.ModeInfo, sig []byte, resolver jsonpb.AnyResolver) error {
	multi := mode.GetMulti()
	if multi == nil {
		return nil
	}
	selected, err := selectedBits(multi.Bitarray)
	if err != nil {
		return err
	}
	if len(multi.ModeInfos) != selected {
		return fmt.Errorf("multisignature has %d mode infos for %d selected keys", len(multi.ModeInfos), selected)
	}
	if err := unknownproto.RejectAmbiguousEncoding(sig, &cryptotypes.MultiSignature{}, resolver); err != nil {
		return err
	}
	// The walk accepted sig, so it holds only signatures (field 1). Read them
	// in place: Unmarshal would copy the signatures at each level of nesting.
	n := 0
	for rest := sig; len(rest) > 0; n++ {
		_, _, tagLen := protowire.ConsumeTag(rest)
		if tagLen < 0 {
			return protowire.ParseError(tagLen)
		}
		inner, innerLen := protowire.ConsumeBytes(rest[tagLen:])
		if innerLen < 0 {
			return protowire.ParseError(innerLen)
		}
		rest = rest[tagLen+innerLen:]
		if n < len(multi.ModeInfos) {
			if err := rejectAmbiguousMultiSignature(multi.ModeInfos[n], inner, resolver); err != nil {
				return err
			}
		}
	}
	if n != len(multi.ModeInfos) {
		return fmt.Errorf("multisignature has %d signatures for %d mode infos", n, len(multi.ModeInfos))
	}
	return nil
}

// selectedBits returns the number of set bits in bits. It rejects a bit array
// that the multisig verifier would read the same way as a different one: an
// ExtraBitsStored that is not below 8 or that has no byte to hold it, or a set
// padding bit after the last logical bit.
func selectedBits(bits *cryptotypes.CompactBitArray) (int, error) {
	if bits == nil {
		return 0, nil
	}
	extra := bits.ExtraBitsStored
	if extra >= 8 {
		return 0, fmt.Errorf("bit array has %d extra bits stored, want less than 8", extra)
	}
	if extra > 0 {
		if len(bits.Elems) == 0 {
			return 0, fmt.Errorf("bit array has %d extra bits stored and no elems", extra)
		}
		if padding := bits.Elems[len(bits.Elems)-1] & (0xff >> extra); padding != 0 {
			return 0, fmt.Errorf("bit array has nonzero padding bits %#x", padding)
		}
	}
	n := 0
	for _, b := range bits.Elems {
		n += mathbits.OnesCount8(b)
	}
	return n, nil
}

// DefaultJSONTxDecoder returns a default protobuf JSON TxDecoder using the provided Marshaler.
func DefaultJSONTxDecoder(cdc codec.Codec) sdk.TxDecoder {
	return func(txBytes []byte) (sdk.Tx, error) {
		var theTx tx.Tx
		err := cdc.UnmarshalJSON(txBytes, &theTx)
		if err != nil {
			return nil, errorsmod.Wrap(sdkerrors.ErrTxDecode, err.Error())
		}

		return &wrapper{
			tx:  &theTx,
			cdc: cdc,
		}, nil
	}
}

// rejectNonADR027TxRaw rejects txBytes that do not follow ADR-027. This is NOT
// a generic ADR-027 checker, it only applies decoding TxRaw. Specifically, it
// only checks that:
// - field numbers are in ascending order (1, 2, and potentially multiple 3s),
// - and varints are as short as possible.
// All other ADR-027 edge cases (e.g. default values) are not applicable with
// TxRaw.
func rejectNonADR027TxRaw(txBytes []byte) error {
	// Make sure all fields are ordered in ascending order with this variable.
	prevTagNum := protowire.Number(0)

	for len(txBytes) > 0 {
		tagNum, wireType, m := protowire.ConsumeTag(txBytes)
		if m < 0 {
			return fmt.Errorf("invalid length; %w", protowire.ParseError(m))
		}
		// TxRaw only has bytes fields.
		if wireType != protowire.BytesType {
			return fmt.Errorf("expected %d wire type, got %d", protowire.BytesType, wireType)
		}
		// Make sure fields are ordered in ascending order.
		if tagNum < prevTagNum {
			return fmt.Errorf("txRaw must follow ADR-027, got tagNum %d after tagNum %d", tagNum, prevTagNum)
		}
		prevTagNum = tagNum

		// All 3 fields of TxRaw have wireType == 2, so their next component
		// is a varint, so we can safely call ConsumeVarint here.
		// Byte structure: <varint of bytes length><bytes sequence>
		// Inner  fields are verified in `DefaultTxDecoder`
		lengthPrefix, m := protowire.ConsumeVarint(txBytes[m:])
		if m < 0 {
			return fmt.Errorf("invalid length; %w", protowire.ParseError(m))
		}
		// We make sure that this varint is as short as possible.
		n := varintMinLength(lengthPrefix)
		if n != m {
			return fmt.Errorf("length prefix varint for tagNum %d is not as short as possible, read %d, only need %d", tagNum, m, n)
		}

		// Skip over the bytes that store fieldNumber and wireType bytes.
		_, _, m = protowire.ConsumeField(txBytes)
		if m < 0 {
			return fmt.Errorf("invalid length; %w", protowire.ParseError(m))
		}
		txBytes = txBytes[m:]
	}

	return nil
}

// varintMinLength returns the minimum number of bytes necessary to encode an
// uint using varint encoding.
func varintMinLength(n uint64) int {
	switch {
	// Note: 1<<N == 2**N.
	case n < 1<<(7):
		return 1
	case n < 1<<(7*2):
		return 2
	case n < 1<<(7*3):
		return 3
	case n < 1<<(7*4):
		return 4
	case n < 1<<(7*5):
		return 5
	case n < 1<<(7*6):
		return 6
	case n < 1<<(7*7):
		return 7
	case n < 1<<(7*8):
		return 8
	case n < 1<<(7*9):
		return 9
	default:
		return 10
	}
}
