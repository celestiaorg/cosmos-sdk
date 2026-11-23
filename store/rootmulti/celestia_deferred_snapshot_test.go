package rootmulti

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/store/iavl"
	snapshottypes "cosmossdk.io/store/snapshots/types"
)

// Commit never waits for a snapshot exporter, even on a mandatory flush.
func TestCelestiaDeferredCommitNeverWaits(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	db := dbm.NewMemDB()
	ms := openDeferredTest(t, db, DeferredAlways, 3, nil)
	ms.SetSnapshotInterval(5)
	for h := int64(1); h <= 5; h++ {
		commitBlock(ms, r, h)
	}
	st := ms.GetCommitKVStore(testStoreKey1).(*iavl.Store)
	exp, err := st.Export(5)
	require.NoError(t, err)
	// Drain first: IAVL's exporter goroutine races with tree mutations
	// (upstream). The exporter holds version 5 as read until Close.
	for {
		if _, err := exp.Next(); err != nil {
			break
		}
	}
	for h := int64(6); h <= 9; h++ {
		commitBlock(ms, r, h)
	}
	done := make(chan struct{})
	go func() { commitBlock(ms, r, 10); close(done) }() // snapshot height: mandatory
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Commit waits for a snapshot exporter")
	}
	require.True(t, st.VersionExists(5), "the exported version stays on disk")
	exp.Close()
	commitBlock(ms, r, 11)
	commitBlock(ms, r, 12)
	require.False(t, st.VersionExists(5))
}

type blockingWriter struct {
	once    sync.Once
	stores  int
	started chan struct{}
	release chan struct{}
}

// WriteMsg blocks at the second store's header: the first store's export is
// done and the later stores are not reached yet.
func (w *blockingWriter) WriteMsg(m proto.Message) error {
	if item, ok := m.(*snapshottypes.SnapshotItem); ok && item.GetStore() != nil {
		w.stores++
		if w.stores == 2 {
			w.once.Do(func() { close(w.started); <-w.release })
		}
	}
	return nil
}
func (w *blockingWriter) Close() error { return nil }

// A snapshot export spanning flushes keeps the snapshot version of every
// store, including the ones it has not reached yet.
func TestCelestiaDeferredSnapshotKeepsAllStores(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	db := dbm.NewMemDB()
	ms := openDeferredTest(t, db, DeferredAlways, 3, nil)
	ms.SetSnapshotInterval(5)
	for h := int64(1); h <= 5; h++ {
		commitBlock(ms, r, h)
	}
	require.Equal(t, int64(5), GetLatestVersion(db))
	w := &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
	errc := make(chan error, 1)
	go func() { errc <- ms.Snapshot(5, w) }()
	<-w.started // the first store is exported; the others are not reached yet
	for h := int64(6); h <= 9; h++ {
		// Change only the exported store: IAVL's exporter races with
		// mutations of the tree it reads (upstream, also without deferred mode).
		commitStore1(ms, r, h)
	}
	st2 := ms.GetCommitKVStore(testStoreKey2).(*iavl.Store)
	t.Logf("store2 has version 5: %v (versions %v)", st2.VersionExists(5), st2.GetAllVersions())
	close(w.release)
	err := <-errc
	t.Logf("snapshot err: %v", err)
	require.NoError(t, err, "snapshot at a snapshot height must succeed")
}

// A later upstream run rewrites the deferred marker, so rollback does not
// trust a stale one.
func TestCelestiaDeferredStaleMarker(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	db := dbm.NewMemDB()
	var h int64
	catchUp := func() time.Time { return headerTime(h).Add(time.Hour) }
	ms := openDeferredTest(t, db, DeferredSync, 10, catchUp)
	for h = 1; h <= 25; h++ {
		commitBlock(ms, r, h)
	}
	h--
	require.True(t, ms.celestiaDeferred.isActive())
	require.NoError(t, ms.Close()) // graceful stop while syncing
	f, def, _ := ReadDeferredMarker(db)
	t.Logf("after close: marker flushed=%d deferred=%v latest=%d", f, def, GetLatestVersion(db))

	// operator sets deferred-persistence = "off"
	ms = openDeferredTest(t, db, DeferredOff, 10, nil)
	for h = 26; h <= 60; h++ {
		commitBlock(ms, r, h)
	}
	h--
	require.NoError(t, ms.Close())
	f, def, _ = ReadDeferredMarker(db)
	st := ms.GetCommitKVStore(testStoreKey1).(*iavl.Store)
	t.Logf("after off run: marker flushed=%d deferred=%v latest=%d versions=%v", f, def, GetLatestVersion(db), len(st.GetAllVersions()))
	// rollback cmd would now take rollbackBehindApp(target=40, lastFlushed=f) => CometBFT-only rollback while app is at 60
	require.False(t, def && f < GetLatestVersion(db), "marker is stale: rollback would roll back CometBFT only below the app height")
}

// Going back to "sync" at the tip keeps the retained history.
func TestCelestiaDeferredSyncAtTipKeepsHistory(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	db := dbm.NewMemDB()
	var h int64
	now := func() time.Time { return headerTime(h).Add(time.Hour) }
	ms := openDeferredTest(t, db, DeferredSync, 10, now)
	for h = 1; h <= 25; h++ {
		commitBlock(ms, r, h)
	}
	require.NoError(t, ms.Close())
	ms = openDeferredTest(t, db, DeferredOff, 10, nil)
	for h = 26; h <= 60; h++ {
		commitBlock(ms, r, h)
	}
	require.NoError(t, ms.Close())
	st := ms.GetCommitKVStore(testStoreKey1).(*iavl.Store)
	before := len(st.GetAllVersions())
	atTip := func() time.Time { return headerTime(h) }
	ms = openDeferredTest(t, db, DeferredSync, 10, atTip)
	st = ms.GetCommitKVStore(testStoreKey1).(*iavl.Store)
	t.Logf("versions before %d, after sync open at tip %d, active=%v", before, len(st.GetAllVersions()), ms.celestiaDeferred.isActive())
	require.Equal(t, before, len(st.GetAllVersions()), "sync mode at the tip must not delete retained history")
}

// Close flushes even while an exporter holds the previous flushed version.
func TestCelestiaDeferredCloseNeverWaits(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	db := dbm.NewMemDB()
	ms := openDeferredTest(t, db, DeferredAlways, 3, nil)
	ms.SetSnapshotInterval(5)
	for h := int64(1); h <= 5; h++ {
		commitBlock(ms, r, h)
	}
	st := ms.GetCommitKVStore(testStoreKey1).(*iavl.Store)
	exp, err := st.Export(5)
	require.NoError(t, err)
	for {
		if _, err := exp.Next(); err != nil {
			break
		}
	}
	for h := int64(6); h <= 8; h++ {
		commitBlock(ms, r, h)
	}
	start := time.Now()
	err = ms.Close()
	t.Logf("close err=%v after %v; db latest=%d, app committed 8", err, time.Since(start), GetLatestVersion(db))
	exp.Close()
	require.Equal(t, int64(8), GetLatestVersion(db))
}

func commitStore1(ms *Store, r *rand.Rand, h int64) {
	ms.SetCommitHeader(cmtproto.Header{Height: h, Time: headerTime(h)})
	cms := ms.CacheMultiStore()
	cms.GetKVStore(testStoreKey1).Set([]byte(fmt.Sprintf("k%03d", r.Intn(150))), []byte(fmt.Sprintf("v%d", h)))
	cms.Write()
	ms.Commit()
}
