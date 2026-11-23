package baseapp

import (
	abci "github.com/cometbft/cometbft/abci/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// celestiaMarkEventsToIndex is sdk.MarkEventsToIndex for the FinalizeBlock
// responses. With blocksync-events-off it returns the events without index
// flags; events are not hashed, but the indexer and RPC clients read them.
func (app *BaseApp) celestiaMarkEventsToIndex(events []abci.Event, indexSet map[string]struct{}) []abci.Event {
	if sdk.BlocksyncEventsOff() {
		return events
	}
	return sdk.MarkEventsToIndex(events, indexSet)
}
