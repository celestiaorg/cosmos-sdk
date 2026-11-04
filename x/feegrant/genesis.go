package feegrant

import (
	errorsmod "cosmossdk.io/errors"

	"github.com/cosmos/cosmos-sdk/codec/types"
)

var _ types.UnpackInterfacesMessage = GenesisState{}

// NewGenesisState creates new GenesisState object
func NewGenesisState(entries []Grant) *GenesisState {
	return &GenesisState{
		Allowances: entries,
	}
}

// ValidateGenesis ensures all grants in the genesis state are valid
func ValidateGenesis(data GenesisState) error {
	for _, f := range data.Allowances {
		grant, err := f.GetGrant()
		if err != nil {
			return err
		}
		if err := validateGenesisAllowance(grant); err != nil {
			return err
		}
	}
	return nil
}

// validateGenesisAllowance runs ValidateBasic, except that a periodic
// allowance, also inside an AllowedMsgAllowance, may list a PeriodSpendLimit
// denom that its Basic.SpendLimit has spent to zero. Accept stores such an
// allowance, so an exported genesis must accept it.
func validateGenesisAllowance(allowance FeeAllowanceI) error {
	switch a := allowance.(type) {
	case *PeriodicAllowance:
		return a.validate(true)
	case *AllowedMsgAllowance:
		if a.Allowance == nil {
			return errorsmod.Wrap(ErrNoAllowance, "allowance should not be empty")
		}
		if len(a.AllowedMessages) == 0 {
			return errorsmod.Wrap(ErrNoMessages, "allowed messages shouldn't be empty")
		}
		inner, err := a.GetAllowance()
		if err != nil {
			return err
		}
		return validateGenesisAllowance(inner)
	default:
		return allowance.ValidateBasic()
	}
}

// DefaultGenesisState returns default state for feegrant module.
func DefaultGenesisState() *GenesisState {
	return &GenesisState{}
}

// UnpackInterfaces implements UnpackInterfacesMessage.UnpackInterfaces
func (data GenesisState) UnpackInterfaces(unpacker types.AnyUnpacker) error {
	for _, f := range data.Allowances {
		err := f.UnpackInterfaces(unpacker)
		if err != nil {
			return err
		}
	}

	return nil
}
