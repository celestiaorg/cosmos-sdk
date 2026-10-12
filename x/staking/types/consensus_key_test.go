package types_test

import (
	"encoding/hex"
	"testing"

	"github.com/oasisprotocol/curve25519-voi/curve"
	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// The vectors below are checked for the property they are named after rather
// than trusted, so a wrong constant fails the test that uses it.
const (
	// order 8, canonical
	smallOrderKey = "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05"
	// the identity, canonical
	identityKey = "0100000000000000000000000000000000000000000000000000000000000000"
	// order 2 (y = p-1), canonical
	orderTwoKey = "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"
	// y = p: a non-canonical spelling of the y = 0 point
	nonCanonicalKey = "edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"
	// non-canonical: masking the sign bit leaves y = 1, the identity
	nonCanonicalIdentitySigned = "eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	// canonical, but no x satisfies the curve equation for this y
	offCurveKey = "0b485cfc0eecc619440448436f8fc9df40566f2369e72400281454cb552afb52"
)

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

func ed25519Key(b []byte) *ed25519.PubKey { return &ed25519.PubKey{Key: b} }

// TestValidateConsensusPubKeyAcceptsGeneratedKeys is the property that matters
// most: the rule must never reject a key produced the ordinary way.
func TestValidateConsensusPubKeyAcceptsGeneratedKeys(t *testing.T) {
	for range 256 {
		pk := ed25519.GenPrivKey().PubKey()
		require.NoError(t, types.ValidateConsensusPubKey(pk))
	}
}

// TestValidateConsensusPubKeyRejectsWeakKeys covers the classes the rule
// exists for. Each vector's defining property is asserted first.
func TestValidateConsensusPubKeyRejectsWeakKeys(t *testing.T) {
	t.Run("small order", func(t *testing.T) {
		key := mustDecode(t, smallOrderKey)
		compressed, err := curve.NewCompressedEdwardsYFromBytes(key)
		require.NoError(t, err)
		require.True(t, compressed.IsCanonicalVartime(), "the vector must be canonical")
		point, err := curve.NewEdwardsPoint().SetCompressedY(compressed)
		require.NoError(t, err)
		require.True(t, point.IsSmallOrder(), "the vector must be small order")

		require.ErrorContains(t, types.ValidateConsensusPubKey(ed25519Key(key)), "small order")
	})

	t.Run("identity", func(t *testing.T) {
		key := mustDecode(t, identityKey)
		point, err := curve.NewEdwardsPoint().SetCompressedY(
			mustCompressed(t, key))
		require.NoError(t, err)
		require.True(t, point.IsIdentity(), "the vector must be the identity")

		require.Error(t, types.ValidateConsensusPubKey(ed25519Key(key)))
	})

	t.Run("order two", func(t *testing.T) {
		key := mustDecode(t, orderTwoKey)
		point, err := curve.NewEdwardsPoint().SetCompressedY(mustCompressed(t, key))
		require.NoError(t, err)
		require.True(t, point.IsSmallOrder())
		require.False(t, point.IsIdentity())

		require.Error(t, types.ValidateConsensusPubKey(ed25519Key(key)))
	})

	t.Run("non canonical", func(t *testing.T) {
		key := mustDecode(t, nonCanonicalKey)
		compressed, err := curve.NewCompressedEdwardsYFromBytes(key)
		require.NoError(t, err)
		require.False(t, compressed.IsCanonicalVartime(), "the vector must be non-canonical")

		require.ErrorContains(t, types.ValidateConsensusPubKey(ed25519Key(key)), "canonically encoded")
	})

	t.Run("non canonical identity with sign bit", func(t *testing.T) {
		key := mustDecode(t, nonCanonicalIdentitySigned)
		compressed, err := curve.NewCompressedEdwardsYFromBytes(key)
		require.NoError(t, err)
		require.False(t, compressed.IsCanonicalVartime(), "the vector must be non-canonical")

		require.ErrorContains(t, types.ValidateConsensusPubKey(ed25519Key(key)), "canonically encoded")
	})

	t.Run("not on the curve", func(t *testing.T) {
		key := mustDecode(t, offCurveKey)
		compressed, err := curve.NewCompressedEdwardsYFromBytes(key)
		require.NoError(t, err)
		require.True(t, compressed.IsCanonicalVartime(), "the vector must be canonical, so only the curve check can reject it")
		_, err = curve.NewEdwardsPoint().SetCompressedY(compressed)
		require.Error(t, err, "the vector must not be a point on the curve")

		require.ErrorContains(t, types.ValidateConsensusPubKey(ed25519Key(key)), "not a point on the curve")
	})

	t.Run("torsion bearing", func(t *testing.T) {
		// an honest key plus a point of order 8: still decodes, still has the
		// right length, but is no longer in the prime-order subgroup.
		honest := ed25519.GenPrivKey().PubKey().(*ed25519.PubKey)
		base, err := curve.NewEdwardsPoint().SetCompressedY(mustCompressed(t, honest.Key))
		require.NoError(t, err)
		torsion, err := curve.NewEdwardsPoint().SetCompressedY(
			mustCompressed(t, mustDecode(t, smallOrderKey)))
		require.NoError(t, err)

		mixed := curve.NewEdwardsPoint().Add(base, torsion)
		require.False(t, mixed.IsTorsionFree(), "the vector must carry torsion")
		require.False(t, mixed.IsSmallOrder(), "and must not be small order itself")
		encoded, err := curve.NewCompressedEdwardsY().SetEdwardsPoint(mixed).MarshalBinary()
		require.NoError(t, err)

		require.ErrorContains(t, types.ValidateConsensusPubKey(ed25519Key(encoded)), "prime-order subgroup")
	})

	t.Run("wrong length", func(t *testing.T) {
		for _, n := range []int{0, 31, 33} {
			require.Error(t, types.ValidateConsensusPubKey(ed25519Key(make([]byte, n))),
				"a %d byte key must be rejected", n)
		}
	})
}

// TestValidateConsensusPubKeyIgnoresOtherKeyTypes pins that this is an Ed25519
// rule: whether another curve may be used at all is the consensus parameter's
// decision, not this function's.
func TestValidateConsensusPubKeyIgnoresOtherKeyTypes(t *testing.T) {
	require.NoError(t, types.ValidateConsensusPubKey(secp256k1.GenPrivKey().PubKey()))
}

func mustCompressed(t *testing.T, b []byte) *curve.CompressedEdwardsY {
	t.Helper()
	c, err := curve.NewCompressedEdwardsYFromBytes(b)
	require.NoError(t, err)
	return c
}
