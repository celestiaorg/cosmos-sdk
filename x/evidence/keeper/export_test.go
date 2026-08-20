package keeper

import (
	"context"

	"cosmossdk.io/x/evidence/types"
)

// HandleEquivocationEvidence exposes handleEquivocationEvidence for testing.
func (k Keeper) HandleEquivocationEvidence(ctx context.Context, evidence *types.Equivocation) error {
	return k.handleEquivocationEvidence(ctx, evidence)
}
