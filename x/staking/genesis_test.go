package staking_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	sdkmath "cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking"
	"github.com/cosmos/cosmos-sdk/x/staking/testutil"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

func TestValidateGenesis(t *testing.T) {
	genValidators1 := make([]types.Validator, 1, 5)
	pk := ed25519.GenPrivKey().PubKey()
	genValidators1[0] = testutil.NewValidator(t, sdk.ValAddress(pk.Address()), pk)
	genValidators1[0].Tokens = sdkmath.OneInt()
	genValidators1[0].DelegatorShares = sdkmath.LegacyOneDec()

	tests := []struct {
		name    string
		mutate  func(*types.GenesisState)
		wantErr bool
	}{
		{"default", func(*types.GenesisState) {}, false},
		// validate genesis validators
		{"duplicate validator", func(data *types.GenesisState) {
			data.Validators = genValidators1
			data.Validators = append(data.Validators, genValidators1[0])
		}, true},
		{"no delegator shares", func(data *types.GenesisState) {
			data.Validators = genValidators1
			data.Validators[0].DelegatorShares = sdkmath.LegacyZeroDec()
		}, true},
		{"jailed and bonded validator", func(data *types.GenesisState) {
			data.Validators = genValidators1
			data.Validators[0].Jailed = true
			data.Validators[0].Status = types.Bonded
		}, true},
		// validate unbonding IDs
		{"unbonding ID records", func(data *types.GenesisState) {
			data.LastUnbondingId = 3
			data.UnbondingIds = []types.UnbondingIdRecord{{Id: 1, UnbondingType: 1, Index: []byte{0x32}}, {Id: 3, UnbondingType: 3}}
		}, false},
		{"exhausted unbonding ID counter", func(data *types.GenesisState) {
			data.LastUnbondingId = math.MaxUint64
		}, true},
		{"unbonding ID zero", func(data *types.GenesisState) {
			data.LastUnbondingId = 3
			data.UnbondingIds = []types.UnbondingIdRecord{{Id: 0, UnbondingType: 1}}
		}, true},
		{"unbonding ID above the counter", func(data *types.GenesisState) {
			data.LastUnbondingId = 3
			data.UnbondingIds = []types.UnbondingIdRecord{{Id: 4, UnbondingType: 1}}
		}, true},
		{"duplicate unbonding ID", func(data *types.GenesisState) {
			data.LastUnbondingId = 3
			data.UnbondingIds = []types.UnbondingIdRecord{{Id: 2, UnbondingType: 1}, {Id: 2, UnbondingType: 2}}
		}, true},
		{"unknown unbonding type", func(data *types.GenesisState) {
			data.LastUnbondingId = 3
			data.UnbondingIds = []types.UnbondingIdRecord{{Id: 2, UnbondingType: 4}}
		}, true},
		{"unbonding ID without a type", func(data *types.GenesisState) {
			data.LastUnbondingId = 3
			data.UnbondingIds = []types.UnbondingIdRecord{{Id: 2, Index: []byte{0x32}}}
		}, true},
	}

	for _, tt := range tests {
		tt := tt

		t.Run(tt.name, func(t *testing.T) {
			genesisState := types.DefaultGenesisState()
			tt.mutate(genesisState)

			if tt.wantErr {
				assert.Error(t, staking.ValidateGenesis(genesisState))
			} else {
				assert.NoError(t, staking.ValidateGenesis(genesisState))
			}
		})
	}
}
