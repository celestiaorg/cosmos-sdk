package types

import (
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/gogoproto/proto"
)

// DiscardEventManager drops every event. BaseApp uses it for recheck, whose
// events nothing reads.
type DiscardEventManager struct{}

var _ EventManagerI = DiscardEventManager{}

func (DiscardEventManager) Events() Events                         { return nil }
func (DiscardEventManager) ABCIEvents() []abci.Event               { return nil }
func (DiscardEventManager) EmitTypedEvent(proto.Message) error     { return nil }
func (DiscardEventManager) EmitTypedEvents(...proto.Message) error { return nil }
func (DiscardEventManager) EmitEvent(Event)                        {}
func (DiscardEventManager) EmitEvents(Events)                      {}

// EventsDiscarded reports whether ctx drops its events, so building them is wasted work.
func EventsDiscarded(ctx Context) bool {
	_, ok := ctx.EventManager().(DiscardEventManager)
	return ok
}
