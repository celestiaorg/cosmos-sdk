package keeper_test

import (
	"math/rand"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/testutil"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestCelestiaHistoricalSortFuzz checks that NewHistoricalInfo sorts random
// validator sets the same way with and without the execution caches.
func (s *KeeperTestSuite) TestCelestiaHistoricalSortFuzz() {
	ctx, k := s.ctx, s.stakingKeeper
	rr := k.PowerReduction(ctx)
	rng := rand.New(rand.NewSource(42)) //nolint:gosec // deterministic test input
	pool := make([]stakingtypes.Validator, 0, 64)
	for i := 0; i < 64; i++ {
		pk := ed25519.GenPrivKey().PubKey()
		pool = append(pool, testutil.NewValidator(s.T(), sdk.ValAddress(pk.Address()), pk))
	}
	for it := 0; it < 2000; it++ {
		vals := make([]stakingtypes.Validator, rng.Intn(64)+1)
		for i := range vals {
			v := pool[rng.Intn(len(pool))] // duplicates allowed
			v.Tokens = k.TokensFromConsensusPower(ctx, int64(rng.Intn(3)))
			if rng.Intn(5) == 0 {
				v.ConsensusPubkey = &codectypes.Any{}
			}
			v.Status = stakingtypes.BondStatus(rng.Intn(4))
			vals[i] = v
		}
		a := append([]stakingtypes.Validator(nil), vals...)
		b := append([]stakingtypes.Validator(nil), vals...)
		restore := setExecutionCaches(false)
		stakingtypes.NewHistoricalInfo(ctx.BlockHeader(), stakingtypes.Validators{Validators: a}, rr)
		restore()
		restore = setExecutionCaches(true)
		stakingtypes.NewHistoricalInfo(ctx.BlockHeader(), stakingtypes.Validators{Validators: b}, rr)
		restore()
		for i := range a {
			if a[i].OperatorAddress != b[i].OperatorAddress || a[i].ConsensusPubkey != b[i].ConsensusPubkey ||
				a[i].Status != b[i].Status || !a[i].Tokens.Equal(b[i].Tokens) {
				s.T().Fatalf("iter %d idx %d differs", it, i)
			}
		}
	}
}
