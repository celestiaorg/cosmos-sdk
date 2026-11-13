package baseapp

// OEInitialized reports whether ProcessProposal started an optimistic execution run.
func (app *BaseApp) OEInitialized() bool { return app.optimisticExec.Initialized() }
