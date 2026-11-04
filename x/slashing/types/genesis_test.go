package types_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/x/slashing/types"
)

// TestValidateGenesisMatchesParamUpdate checks that genesis validation accepts
// every value a parameter update accepts, and rejects what it rejects.
func TestValidateGenesisMatchesParamUpdate(t *testing.T) {
	testCases := []struct {
		name   string
		modify func(*types.Params)
		expErr bool
	}{
		{"default", func(*types.Params) {}, false},
		{"window below 10", func(p *types.Params) { p.SignedBlocksWindow = 5 }, false},
		{"jail below a minute", func(p *types.Params) { p.DowntimeJailDuration = 30 * time.Second }, false},
		{"zero window", func(p *types.Params) { p.SignedBlocksWindow = 0 }, true},
		{"zero jail", func(p *types.Params) { p.DowntimeJailDuration = 0 }, true},
		{"fraction above one", func(p *types.Params) { p.SlashFractionDowntime = math.LegacyNewDec(2) }, true},
		{"nil fraction", func(p *types.Params) { p.SlashFractionDoubleSign = math.LegacyDec{} }, true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params := types.DefaultParams()
			tc.modify(&params)
			require.Equal(t, params.Validate() != nil, tc.expErr, "parameter update")
			gs := types.DefaultGenesisState()
			gs.Params = params
			err := types.ValidateGenesis(*gs)
			if tc.expErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
