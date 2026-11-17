package baseapp

import (
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	protoio "github.com/cosmos/gogoproto/io"

	snapshottypes "cosmossdk.io/store/snapshots/types"
)

// appSnapshotter refreshes BaseApp's state for both ABCI and local restores.
type appSnapshotter struct {
	snapshottypes.Snapshotter
	app *BaseApp
}

func (s appSnapshotter) Restore(height uint64, format uint32, reader protoio.Reader) (snapshottypes.SnapshotItem, error) {
	item, err := s.Snapshotter.Restore(height, format, reader)
	if err != nil {
		return item, err
	}

	// checkState still branches the pre-restore stores; rebuild it as Init does.
	s.app.checkStateMu.Lock()
	defer s.app.checkStateMu.Unlock()
	s.app.setState(execModeCheck, cmtproto.Header{ChainID: s.app.chainID})
	return item, nil
}
