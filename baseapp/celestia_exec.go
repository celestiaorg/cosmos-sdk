package baseapp

import (
	abci "github.com/cometbft/cometbft/abci/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// celestiaMarkEventsToIndex is sdk.MarkEventsToIndex for the finalize-mode
// responses (BeginBlock, tx results, EndBlock). With CELESTIA_BLOCKSYNC_EVENTS_OFF
// the events are returned as emitted, without the per-attribute index flags:
// events and their index flags are not part of any hash (the results hash covers
// only Code/Data/GasWanted/GasUsed) and are only used by the tx indexer.
func (app *BaseApp) celestiaMarkEventsToIndex(events []abci.Event, indexSet map[string]struct{}) []abci.Event {
	if sdk.CelestiaBlocksyncEventsOff {
		return events
	}
	return sdk.MarkEventsToIndex(events, indexSet)
}
