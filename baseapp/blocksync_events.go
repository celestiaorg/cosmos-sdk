package baseapp

import (
	abci "github.com/tendermint/tendermint/abci/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// celestiaMarkEventsToIndex is sdk.MarkEventsToIndex for the deliver-mode
// responses (BeginBlock, DeliverTx, EndBlock). With sdk.BlocksyncEventsOff
// the events are returned as emitted, without the per-attribute index flags:
// events and their index flags are not part of any hash (the results hash covers
// only Code/Data/GasWanted/GasUsed) and are only used by the tx indexer.
func (app *BaseApp) celestiaMarkEventsToIndex(events []abci.Event) []abci.Event {
	if sdk.BlocksyncEventsOff() {
		return events
	}
	return sdk.MarkEventsToIndex(events, app.indexEvents)
}

// celestiaSkipMsgLogs reports whether runMsgs may skip building the
// (unhashed) ABCIMessageLogs JSON string for the response Log field.
func celestiaSkipMsgLogs(mode runTxMode) bool {
	return mode == runTxModeDeliver && sdk.BlocksyncEventsOff()
}
