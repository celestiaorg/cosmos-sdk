package staking

import (
	"fmt"
	"math"

	cmttypes "github.com/cometbft/cometbft/types"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// WriteValidators returns a slice of bonded genesis validators.
func WriteValidators(ctx sdk.Context, keeper *keeper.Keeper) (vals []cmttypes.GenesisValidator, returnErr error) {
	err := keeper.IterateLastValidators(ctx, func(_ int64, validator types.ValidatorI) (stop bool) {
		pk, err := validator.ConsPubKey()
		if err != nil {
			returnErr = err
			return true
		}
		cmtPk, err := cryptocodec.ToCmtPubKeyInterface(pk)
		if err != nil {
			returnErr = err
			return true
		}

		vals = append(vals, cmttypes.GenesisValidator{
			Address: sdk.ConsAddress(cmtPk.Address()).Bytes(),
			PubKey:  cmtPk,
			Power:   validator.GetConsensusPower(keeper.PowerReduction(ctx)),
			Name:    validator.GetMoniker(),
		})

		return false
	})
	if err != nil {
		return nil, err
	}

	return
}

// ValidateGenesis validates the provided staking genesis state to ensure the
// expected invariants holds. (i.e. params in correct bounds, no duplicate validators)
func ValidateGenesis(data *types.GenesisState) error {
	if err := validateGenesisStateValidators(data.Validators); err != nil {
		return err
	}
	if err := validateUnbondingIDs(data); err != nil {
		return err
	}

	return data.Params.Validate()
}

// validateUnbondingIDs checks the unbonding ID counter and records: the
// counter must leave an ID to issue, and each record is a distinct issued ID
// with an index or a known type.
func validateUnbondingIDs(data *types.GenesisState) error {
	if data.LastUnbondingId == math.MaxUint64 {
		return fmt.Errorf("last unbonding ID %d leaves no ID to issue", data.LastUnbondingId)
	}
	seen := make(map[uint64]struct{}, len(data.UnbondingIds))
	for _, record := range data.UnbondingIds {
		if record.Id == 0 || record.Id > data.LastUnbondingId {
			return fmt.Errorf("unbonding ID %d is not between 1 and the last unbonding ID %d", record.Id, data.LastUnbondingId)
		}
		if _, ok := seen[record.Id]; ok {
			return fmt.Errorf("duplicate unbonding ID %d", record.Id)
		}
		seen[record.Id] = struct{}{}
		if record.UnbondingType > uint64(types.UnbondingType_ValidatorUnbonding) {
			return fmt.Errorf("unbonding ID %d has unknown type %d", record.Id, record.UnbondingType)
		}
		if record.UnbondingType == 0 && len(record.Index) == 0 {
			return fmt.Errorf("unbonding ID %d has neither an index nor a type", record.Id)
		}
	}
	return nil
}

func validateGenesisStateValidators(validators []types.Validator) error {
	addrMap := make(map[string]bool, len(validators))

	for i := 0; i < len(validators); i++ {
		val := validators[i]
		consPk, err := val.ConsPubKey()
		if err != nil {
			return err
		}

		// The same rule CreateValidator applies, so a genesis file or a gentx
		// cannot seat a key a transaction could not register.
		if err := types.ValidateConsensusPubKey(consPk); err != nil {
			return err
		}

		strKey := string(consPk.Bytes())

		if _, ok := addrMap[strKey]; ok {
			consAddr, err := val.GetConsAddr()
			if err != nil {
				return err
			}
			return fmt.Errorf("duplicate validator in genesis state: moniker %v, address %v", val.Description.Moniker, consAddr)
		}

		if val.Jailed && val.IsBonded() {
			consAddr, err := val.GetConsAddr()
			if err != nil {
				return err
			}
			return fmt.Errorf("validator is bonded and jailed in genesis state: moniker %v, address %v", val.Description.Moniker, consAddr)
		}

		if val.DelegatorShares.IsZero() && !val.IsUnbonding() {
			return fmt.Errorf("bonded/unbonded genesis validator cannot have zero delegator shares, validator: %v", val)
		}

		addrMap[strKey] = true
	}

	return nil
}
