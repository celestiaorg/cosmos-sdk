package types

import (
	"bytes"
	"sort"

	"cosmossdk.io/math"
)

// celestiaSortByVotingPower sorts vals like
//
//	sort.SliceStable(vals, func(i, j int) bool { return ValidatorsByVotingPower(vals).Less(i, j, r) })
//
// with each element's ConsensusPower and GetConsAddr computed once up front
// instead of on every comparison. Both are pure and the comparator gives the
// same result for every pair, so the order is the same. With two or more
// validators every element is compared, so computing them eagerly reaches the
// same panics as the stock sort; validators in the last validator set always
// have a consensus pubkey. Used when sdk.ExecutionCachesEnabled.
func celestiaSortByVotingPower(vals []Validator, r math.Int) {
	if len(vals) < 2 {
		return // the stock sort does no comparisons
	}
	type entry struct {
		val     Validator
		power   int64
		addr    []byte
		addrErr error
	}
	es := make([]entry, len(vals))
	for i, v := range vals {
		addr, err := v.GetConsAddr()
		es[i] = entry{val: v, power: v.ConsensusPower(r), addr: addr, addrErr: err}
	}
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].power == es[j].power {
			// If either returns error, then return false
			if es[i].addrErr != nil || es[j].addrErr != nil {
				return false
			}
			return bytes.Compare(es[i].addr, es[j].addr) == -1
		}
		return es[i].power > es[j].power
	})
	for i := range es {
		vals[i] = es[i].val
	}
}
