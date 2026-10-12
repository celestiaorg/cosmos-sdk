package types

import (
	"github.com/oasisprotocol/curve25519-voi/curve"

	errorsmod "cosmossdk.io/errors"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

// ValidateConsensusPubKey requires an Ed25519 consensus key to be a canonical
// encoding of a point in the prime-order subgroup, which an honestly generated
// key always is. Keys of other types pass through.
//
// Signature verification is ZIP-215: it accepts small-order and
// non-canonically encoded keys on purpose, so that batch and single
// verification agree. That is the right rule for checking a signature against
// a key the chain already has, and the wrong one for admitting a new key:
//
//   - a small-order key verifies signatures nobody produced, so anyone could
//     speak for that validator;
//   - A+T, for an honest key A and an order-8 point T, verifies every
//     signature A produces — a second validator slot, at a consensus address
//     the duplicate-key check does not catch, signed for unknowingly by the
//     owner of A.
func ValidateConsensusPubKey(pk cryptotypes.PubKey) error {
	key, ok := pk.(*ed25519.PubKey)
	if !ok {
		return nil
	}
	return validateEd25519ConsensusKey(key.Key)
}

func validateEd25519ConsensusKey(keyBytes []byte) error {
	if len(keyBytes) != ed25519.PubKeySize {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidPubKey,
			"ed25519 consensus key must be %d bytes, got %d", ed25519.PubKeySize, len(keyBytes))
	}

	// Only fails on a wrong length, which the check above already rejected.
	compressed, err := curve.NewCompressedEdwardsYFromBytes(keyBytes)
	if err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidPubKey, "ed25519 consensus key is not a valid encoding")
	}
	if !compressed.IsCanonicalVartime() {
		return errorsmod.Wrap(sdkerrors.ErrInvalidPubKey,
			"ed25519 consensus key is not canonically encoded; the same key would have several consensus addresses")
	}

	point, err := curve.NewEdwardsPoint().SetCompressedY(compressed)
	if err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidPubKey, "ed25519 consensus key is not a point on the curve")
	}
	// The identity is the only point that passes IsTorsionFree and still must
	// be rejected, so this clause is not redundant with the one below.
	if point.IsSmallOrder() {
		return errorsmod.Wrap(sdkerrors.ErrInvalidPubKey,
			"ed25519 consensus key has small order; it would verify signatures nobody produced")
	}
	if !point.IsTorsionFree() {
		return errorsmod.Wrap(sdkerrors.ErrInvalidPubKey,
			"ed25519 consensus key is not in the prime-order subgroup; it would verify another validator's signatures")
	}
	return nil
}
