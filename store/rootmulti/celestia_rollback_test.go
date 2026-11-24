package rootmulti

import (
	"math/rand"
	"testing"
	"time"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"
)

// TestCelestiaRollbackCheckAfterLeavingSync: leaving "sync" at the tip
// prunes every tree to the flushed version S, so a rollback to S-1 must be
// refused before anything is written; one commit later, a rollback to S works.
func TestCelestiaRollbackCheckAfterLeavingSync(t *testing.T) {
	r := rand.New(rand.NewSource(7)) //nolint:gosec // deterministic test data
	db := dbm.NewMemDB()
	var h int64
	lag := time.Hour
	now := func() time.Time { return headerTime(h).Add(lag) }
	ms := openDeferredTest(t, db, DeferredSync, 10, now)
	for h = 1; h <= 25; h++ {
		commitBlock(ms, r, h)
	}
	require.True(t, ms.celestiaDeferred.isActive())
	lag = 0
	const leftAt = 26
	commitBlock(ms, r, leftAt) // caught up: flushes and leaves deferred mode
	require.False(t, ms.celestiaDeferred.isActive())
	require.NoError(t, ms.Close())

	ms = openDeferredTest(t, db, DeferredOff, 0, nil)
	before := dbDigest(t, db)
	_, err := ms.CheckRollbackToVersion(leftAt - 1)
	require.ErrorContains(t, err, "version 25") // no commit info and no IAVL version at S-1
	_, err = ms.CheckRollbackToVersion(20) // commit info from the flush at 20, IAVL version pruned
	require.ErrorContains(t, err, "has no version 20")
	require.Equal(t, before, dbDigest(t, db), "the check writes nothing")

	commitBlock(ms, r, leftAt+1)
	hash, err := ms.CheckRollbackToVersion(leftAt)
	require.NoError(t, err)
	cInfo, err := ms.GetCommitInfo(leftAt)
	require.NoError(t, err)
	require.Equal(t, cInfo.CommitID().Hash, hash)
	require.NoError(t, ms.RollbackToVersion(leftAt))
	require.Equal(t, int64(leftAt), ms.LastCommitID().Version)
}

// TestCelestiaRollbackCheckDeferred: a DB in deferred state is refused.
func TestCelestiaRollbackCheckDeferred(t *testing.T) {
	r := rand.New(rand.NewSource(8)) //nolint:gosec // deterministic test data
	db := dbm.NewMemDB()
	ms := openDeferredTest(t, db, DeferredSync, 5, func() time.Time { return headerTime(10_000) })
	for h := int64(1); h <= 12; h++ {
		commitBlock(ms, r, h)
	}
	var rbErr ErrDeferredRollback
	_, err := ms.CheckRollbackToVersion(11)
	require.ErrorAs(t, err, &rbErr)
	_, err = ms.CheckRollbackToVersion(0)
	require.ErrorContains(t, err, "invalid rollback height")
}
