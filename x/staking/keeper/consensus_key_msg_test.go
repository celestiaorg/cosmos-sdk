package keeper_test

import (
	"encoding/hex"

	"github.com/oasisprotocol/curve25519-voi/curve"

	"cosmossdk.io/math"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestMsgCreateValidatorRejectsWeakConsensusKey pins the wiring, not the
// predicate: a weak key must be refused by the message server itself, and
// nothing may be written when it is.
func (s *KeeperTestSuite) TestMsgCreateValidatorRejectsWeakConsensusKey() {
	require := s.Require()

	// an order-8 point, and an honest key with that point added to it
	smallOrder, err := hex.DecodeString("26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05")
	require.NoError(err)
	torsion, err := curve.NewEdwardsPoint().SetCompressedY(compressedFromBytes(s, smallOrder))
	require.NoError(err)
	honest := ed25519.GenPrivKey().PubKey().(*ed25519.PubKey)
	base, err := curve.NewEdwardsPoint().SetCompressedY(compressedFromBytes(s, honest.Key))
	require.NoError(err)
	mixed, err := curve.NewCompressedEdwardsY().SetEdwardsPoint(
		curve.NewEdwardsPoint().Add(base, torsion)).MarshalBinary()
	require.NoError(err)

	for _, tc := range []struct {
		name string
		key  []byte
	}{
		{"small order", smallOrder},
		{"torsion bearing", mixed},
	} {
		s.Run(tc.name, func() {
			s.execExpectCalls()
			pubkey, err := codectypes.NewAnyWithValue(&ed25519.PubKey{Key: tc.key})
			require.NoError(err)

			_, err = s.msgServer.CreateValidator(s.ctx, &stakingtypes.MsgCreateValidator{
				Description:       stakingtypes.Description{Moniker: "NewValidator"},
				Commission:        stakingtypes.CommissionRates{Rate: math.LegacyNewDecWithPrec(5, 1), MaxRate: math.LegacyNewDecWithPrec(5, 1), MaxChangeRate: math.LegacyNewDec(0)},
				MinSelfDelegation: math.NewInt(1),
				DelegatorAddress:  Addr.String(),
				ValidatorAddress:  ValAddr.String(),
				Pubkey:            pubkey,
				Value:             sdk.NewInt64Coin("stake", 10000),
			})
			require.Error(err)
			require.ErrorContains(err, "invalid pubkey")

			// nothing may be left behind by a rejected message
			_, err = s.stakingKeeper.GetValidator(s.ctx, ValAddr)
			require.Error(err)
		})
	}
}

// TestMsgCreateValidatorAcceptsGeneratedConsensusKey is the other half: the
// rule must not stand in the way of an ordinary key.
func (s *KeeperTestSuite) TestMsgCreateValidatorAcceptsGeneratedConsensusKey() {
	require := s.Require()
	s.execExpectCalls()

	pubkey, err := codectypes.NewAnyWithValue(ed25519.GenPrivKey().PubKey())
	require.NoError(err)

	_, err = s.msgServer.CreateValidator(s.ctx, &stakingtypes.MsgCreateValidator{
		Description:       stakingtypes.Description{Moniker: "NewValidator"},
		Commission:        stakingtypes.CommissionRates{Rate: math.LegacyNewDecWithPrec(5, 1), MaxRate: math.LegacyNewDecWithPrec(5, 1), MaxChangeRate: math.LegacyNewDec(0)},
		MinSelfDelegation: math.NewInt(1),
		DelegatorAddress:  Addr.String(),
		ValidatorAddress:  ValAddr.String(),
		Pubkey:            pubkey,
		Value:             sdk.NewInt64Coin("stake", 10000),
	})
	require.NoError(err)
}

func compressedFromBytes(s *KeeperTestSuite, b []byte) *curve.CompressedEdwardsY {
	c, err := curve.NewCompressedEdwardsYFromBytes(b)
	s.Require().NoError(err)
	return c
}
