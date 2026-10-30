package types_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/x/distribution/types"
)

func TestGenesisWithoutUserOutstandingRewards(t *testing.T) {
	cdc := codec.NewProtoCodec(codectypes.NewInterfaceRegistry())

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(cdc.MustMarshalJSON(types.DefaultGenesisState()), &raw))
	require.Contains(t, raw, "user_outstanding_rewards")
	delete(raw, "user_outstanding_rewards")
	bz, err := json.Marshal(raw)
	require.NoError(t, err)

	var gs types.GenesisState
	require.NoError(t, cdc.UnmarshalJSON(bz, &gs))
	require.Empty(t, gs.UserOutstandingRewards)
	require.NoError(t, types.ValidateGenesis(&gs))
}
