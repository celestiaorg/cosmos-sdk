package server

import (
	cmtcmd "github.com/cometbft/cometbft/cmd/cometbft/commands"
	cmtcfg "github.com/cometbft/cometbft/config"
)

// SetRollbackCometState replaces the CometBFT rollback until restore is called.
func SetRollbackCometState(f func(cfg *cmtcfg.Config, height int64, removeBlock bool) (int64, []byte, error)) (restore func()) {
	rollbackCometState = f
	return func() { rollbackCometState = cmtcmd.RollbackStateToHeight }
}
