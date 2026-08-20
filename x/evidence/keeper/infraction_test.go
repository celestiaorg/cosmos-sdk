package keeper_test

import (
	"errors"
	"time"

	"github.com/golang/mock/gomock"

	"cosmossdk.io/x/evidence/types"

	"github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestHandleEquivocationEvidenceDeletedValidator verifies that evidence naming
// a validator whose record no longer exists in staking state is ignored
// instead of returning an error.
func (suite *KeeperTestSuite) TestHandleEquivocationEvidenceDeletedValidator() {
	pk := ed25519.GenPrivKey()
	evidence := &types.Equivocation{
		Height:           1,
		Power:            100,
		Time:             time.Now().UTC(),
		ConsensusAddress: sdk.ConsAddress(pk.PubKey().Address().Bytes()).String(),
	}

	suite.stakingKeeper.EXPECT().ConsensusAddressCodec().Return(address.NewBech32Codec(sdk.Bech32PrefixConsAddr)).AnyTimes()
	// The staking keeper returns a zero-value validator alongside
	// ErrNoValidatorFound once the validator's record has been deleted.
	suite.stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), gomock.Any()).Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound)

	err := suite.evidenceKeeper.HandleEquivocationEvidence(suite.ctx, evidence)
	suite.Require().NoError(err)
}

// TestHandleEquivocationEvidenceStakingError verifies that errors other than
// ErrNoValidatorFound from the staking keeper still propagate.
func (suite *KeeperTestSuite) TestHandleEquivocationEvidenceStakingError() {
	pk := ed25519.GenPrivKey()
	evidence := &types.Equivocation{
		Height:           1,
		Power:            100,
		Time:             time.Now().UTC(),
		ConsensusAddress: sdk.ConsAddress(pk.PubKey().Address().Bytes()).String(),
	}

	suite.stakingKeeper.EXPECT().ConsensusAddressCodec().Return(address.NewBech32Codec(sdk.Bech32PrefixConsAddr)).AnyTimes()
	suite.stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), gomock.Any()).Return(stakingtypes.Validator{}, errors.New("boom"))

	err := suite.evidenceKeeper.HandleEquivocationEvidence(suite.ctx, evidence)
	suite.Require().ErrorContains(err, "boom")
}
