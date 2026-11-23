package address

import (
	"errors"
	"slices"
	"strings"

	"cosmossdk.io/core/address"
	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

type Bech32Codec struct {
	Bech32Prefix string
}

var _ address.Codec = &Bech32Codec{}

func NewBech32Codec(prefix string) address.Codec {
	return Bech32Codec{prefix}
}

// StringToBytes encodes text to bytes
func (bc Bech32Codec) StringToBytes(text string) ([]byte, error) {
	if len(strings.TrimSpace(text)) == 0 {
		return []byte{}, errors.New("empty address string is not allowed")
	}

	cache := sdk.IsAddrCacheEnabled() && len(text) <= codecCacheMaxLen
	if cache {
		if d, ok := decodeCache.get(text); ok && d.hrp == bc.Bech32Prefix {
			if err := sdk.VerifyAddressFormat(d.bz); err != nil {
				return nil, err
			}
			return slices.Clone(d.bz), nil
		}
	}

	hrp, bz, err := bech32.DecodeAndConvert(text)
	if err != nil {
		return nil, err
	}

	if hrp != bc.Bech32Prefix {
		return nil, errorsmod.Wrapf(sdkerrors.ErrLogic, "hrp does not match bech32 prefix: expected '%s' got '%s'", bc.Bech32Prefix, hrp)
	}

	if err := sdk.VerifyAddressFormat(bz); err != nil {
		return nil, err
	}

	if cache {
		decodeCache.put(text, bech32Decoded{hrp: hrp, bz: slices.Clone(bz)})
	}
	return bz, nil
}

// BytesToString decodes bytes to text
func (bc Bech32Codec) BytesToString(bz []byte) (string, error) {
	if len(bz) == 0 {
		return "", nil
	}

	cache := sdk.IsAddrCacheEnabled() && len(bz) <= codecCacheMaxBytes
	if cache {
		if text, ok := encodeCache.get(encodeCacheKey(bc.Bech32Prefix, bz)); ok {
			return text, nil
		}
	}

	text, err := bech32.ConvertAndEncode(bc.Bech32Prefix, bz)
	if err != nil {
		return "", err
	}

	if cache && len(text) <= codecCacheMaxLen {
		encodeCache.put(encodeCacheKey(bc.Bech32Prefix, bz), text)
	}

	return text, nil
}
