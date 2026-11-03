package types

import (
	"errors"
	"fmt"

	"cosmossdk.io/core/address"

	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

func NewGenesisState(
	params Params, fp FeePool, dwis []DelegatorWithdrawInfo, pp sdk.ConsAddress, r []ValidatorOutstandingRewardsRecord,
	acc []ValidatorAccumulatedCommissionRecord, historical []ValidatorHistoricalRewardsRecord,
	cur []ValidatorCurrentRewardsRecord, dels []DelegatorStartingInfoRecord, slashes []ValidatorSlashEventRecord,
) *GenesisState {
	return &GenesisState{
		Params:                          params,
		FeePool:                         fp,
		DelegatorWithdrawInfos:          dwis,
		PreviousProposer:                pp.String(),
		OutstandingRewards:              r,
		ValidatorAccumulatedCommissions: acc,
		ValidatorHistoricalRewards:      historical,
		ValidatorCurrentRewards:         cur,
		DelegatorStartingInfos:          dels,
		ValidatorSlashEvents:            slashes,
	}
}

// get raw genesis raw message for testing
func DefaultGenesisState() *GenesisState {
	return &GenesisState{
		FeePool:                         InitialFeePool(),
		Params:                          DefaultParams(),
		DelegatorWithdrawInfos:          []DelegatorWithdrawInfo{},
		PreviousProposer:                "",
		OutstandingRewards:              []ValidatorOutstandingRewardsRecord{},
		ValidatorAccumulatedCommissions: []ValidatorAccumulatedCommissionRecord{},
		ValidatorHistoricalRewards:      []ValidatorHistoricalRewardsRecord{},
		ValidatorCurrentRewards:         []ValidatorCurrentRewardsRecord{},
		DelegatorStartingInfos:          []DelegatorStartingInfoRecord{},
		ValidatorSlashEvents:            []ValidatorSlashEventRecord{},
		UserOutstandingRewards:          []UserOutstandingRewardsRecord{},
	}
}

// ValidateGenesis validates the genesis state of distribution genesis input
func ValidateGenesis(gs *GenesisState) error {
	if err := gs.Params.ValidateBasic(); err != nil {
		return err
	}
	if err := gs.FeePool.ValidateGenesis(); err != nil {
		return err
	}
	cfg := sdk.GetConfig()
	return ValidateUserOutstandingRewards(
		gs.UserOutstandingRewards,
		addresscodec.NewBech32Codec(cfg.GetBech32AccountAddrPrefix()),
		addresscodec.NewBech32Codec(cfg.GetBech32ValidatorAddrPrefix()),
	)
}

// ValidateUserOutstandingRewards checks the user outstanding rewards records of a genesis state.
func ValidateUserOutstandingRewards(records []UserOutstandingRewardsRecord, ac, vc address.Codec) error {
	type pair struct{ del, val string }
	seen := make(map[pair]struct{}, len(records))
	for i, r := range records {
		del, err := ac.StringToBytes(r.DelegatorAddress)
		if err != nil {
			return userOutstandingRewardsError(i, fmt.Errorf("invalid delegator address %q: %w", r.DelegatorAddress, err))
		}
		val, err := vc.StringToBytes(r.ValidatorAddress)
		if err != nil {
			return userOutstandingRewardsError(i, fmt.Errorf("invalid validator address %q: %w", r.ValidatorAddress, err))
		}
		key := pair{string(del), string(val)}
		if _, ok := seen[key]; ok {
			return userOutstandingRewardsError(i, fmt.Errorf("duplicate delegator %s and validator %s", r.DelegatorAddress, r.ValidatorAddress))
		}
		seen[key] = struct{}{}

		// Validate each coin first: Coins.Validate panics on a nil amount.
		for _, coin := range r.Rewards {
			if err := coin.Validate(); err != nil {
				return userOutstandingRewardsError(i, fmt.Errorf("invalid reward coin: %w", err))
			}
		}
		if err := r.Rewards.Validate(); err != nil {
			return userOutstandingRewardsError(i, fmt.Errorf("invalid rewards: %w", err))
		}
		if len(r.Rewards) == 0 {
			return userOutstandingRewardsError(i, errors.New("empty rewards"))
		}
	}
	return nil
}

func userOutstandingRewardsError(i int, err error) error {
	return fmt.Errorf("%s genesis: user outstanding rewards record %d: %w", ModuleName, i, err)
}
