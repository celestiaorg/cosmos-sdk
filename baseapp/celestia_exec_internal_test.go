package baseapp

import (
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func TestCelestiaMarkEventsToIndex(t *testing.T) {
	app := &BaseApp{}
	events := func() []abci.Event {
		return []abci.Event{{Type: "transfer", Attributes: []abci.EventAttribute{{Key: "amount", Value: "1stake"}}}}
	}

	marked := app.celestiaMarkEventsToIndex(events(), nil)
	require.Equal(t, sdk.MarkEventsToIndex(events(), nil), marked)
	require.True(t, marked[0].Attributes[0].Index)

	sdk.SetBlocksyncEventsOff(true)
	defer sdk.SetBlocksyncEventsOff(false)
	require.Equal(t, events(), app.celestiaMarkEventsToIndex(events(), nil))
}
