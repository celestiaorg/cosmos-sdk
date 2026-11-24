package types

import (
	"bytes"
	"sort"

	"cosmossdk.io/math"
)

// celestiaSortByVotingPower sorts vals exactly like
//
//	sort.SliceStable(vals, func(i, j int) bool { return ValidatorsByVotingPower(vals).Less(i, j, r) })
//
// (same algorithm, same comparator results for every pair, so the same order),
// but memoizes each element's ConsensusPower and GetConsAddr (a pubkey hash)
// instead of recomputing them on every comparison. Both are pure; they are
// computed lazily, at the first comparison that needs them, as upstream does.
// Used when the execution caches are on.
func celestiaSortByVotingPower(vals []Validator, r math.Int) {
	type entry struct {
		val      Validator
		power    int64
		hasPower bool
		addr     []byte
		addrErr  error
		hasAddr  bool
	}
	es := make([]entry, len(vals))
	for i := range vals {
		es[i].val = vals[i]
	}
	power := func(e *entry) int64 {
		if !e.hasPower {
			e.power, e.hasPower = e.val.ConsensusPower(r), true
		}
		return e.power
	}
	consAddr := func(e *entry) ([]byte, error) {
		if !e.hasAddr {
			a, err := e.val.GetConsAddr()
			e.addr, e.addrErr, e.hasAddr = a, err, true
		}
		return e.addr, e.addrErr
	}
	sort.SliceStable(es, func(i, j int) bool {
		ei, ej := &es[i], &es[j]
		if power(ei) == power(ej) {
			addrI, errI := consAddr(ei)
			addrJ, errJ := consAddr(ej)
			// If either returns error, then return false
			if errI != nil || errJ != nil {
				return false
			}
			return bytes.Compare(addrI, addrJ) == -1
		}
		return power(ei) > power(ej)
	})
	for i := range es {
		vals[i] = es[i].val
	}
}
