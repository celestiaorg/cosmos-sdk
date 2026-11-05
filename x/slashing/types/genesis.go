package types

// NewGenesisState creates a new GenesisState object
func NewGenesisState(
	params Params, signingInfos []SigningInfo, missedBlocks []ValidatorMissedBlocks,
) *GenesisState {
	return &GenesisState{
		Params:       params,
		SigningInfos: signingInfos,
		MissedBlocks: missedBlocks,
	}
}

// NewMissedBlock creates a new MissedBlock instance
func NewMissedBlock(index int64, missed bool) MissedBlock {
	return MissedBlock{
		Index:  index,
		Missed: missed,
	}
}

// DefaultGenesisState - default GenesisState used by Cosmos Hub
func DefaultGenesisState() *GenesisState {
	return &GenesisState{
		Params:       DefaultParams(),
		SigningInfos: []SigningInfo{},
		MissedBlocks: []ValidatorMissedBlocks{},
	}
}

// ValidateGenesis validates the slashing genesis parameters with the same
// rules as a parameter update, so a genesis exported after governance set any
// accepted value still validates.
func ValidateGenesis(data GenesisState) error {
	return data.Params.Validate()
}
