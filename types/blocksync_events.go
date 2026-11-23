package types

import "sync/atomic"

var blocksyncEventsOff atomic.Bool

// BlocksyncEventsOff reports whether deliver-mode responses skip the
// unhashed parts of events and logs (see SetBlocksyncEventsOff). Off by default.
func BlocksyncEventsOff() bool { return blocksyncEventsOff.Load() }

// SetBlocksyncEventsOff makes BeginBlock, DeliverTx and EndBlock responses
// skip event index flags, the DeliverTx Log and distribution amount strings.
// None of these are hashed, but the tx indexer and RPC clients read them, so
// it is for syncing nodes that serve no queries. Nodes set it at startup from
// app.toml [performance] blocksync-events-off.
func SetBlocksyncEventsOff(off bool) { blocksyncEventsOff.Store(off) }
