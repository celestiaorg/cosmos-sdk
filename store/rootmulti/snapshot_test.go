package rootmulti_test

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"testing"
	"time"

	dbm "github.com/cosmos/cosmos-db"
	protoio "github.com/cosmos/gogoproto/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/iavl"
	"cosmossdk.io/store/metrics"
	"cosmossdk.io/store/rootmulti"
	"cosmossdk.io/store/snapshots"
	snapshottypes "cosmossdk.io/store/snapshots/types"
	"cosmossdk.io/store/types"
)

func newMultiStoreWithGeneratedData(db dbm.DB, stores uint8, storeKeys uint64) *rootmulti.Store {
	multiStore := rootmulti.NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	r := rand.New(rand.NewSource(49872768940)) // Fixed seed for deterministic tests

	keys := []*types.KVStoreKey{}
	for i := uint8(0); i < stores; i++ {
		key := types.NewKVStoreKey(fmt.Sprintf("store%v", i))
		multiStore.MountStoreWithDB(key, types.StoreTypeIAVL, nil)
		keys = append(keys, key)
	}
	err := multiStore.LoadLatestVersion()
	if err != nil {
		panic(err)
	}

	for _, key := range keys {
		store := multiStore.GetCommitKVStore(key).(*iavl.Store)
		for i := uint64(0); i < storeKeys; i++ {
			k := make([]byte, 8)
			v := make([]byte, 1024)
			binary.BigEndian.PutUint64(k, i)
			_, err := r.Read(v)
			if err != nil {
				panic(err)
			}
			store.Set(k, v)
		}
	}

	multiStore.Commit()
	err = multiStore.LoadLatestVersion()
	if err != nil {
		panic(err)
	}

	return multiStore
}

func newMultiStoreWithMixedMounts(db dbm.DB) *rootmulti.Store {
	store := rootmulti.NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	store.MountStoreWithDB(types.NewKVStoreKey("iavl1"), types.StoreTypeIAVL, nil)
	store.MountStoreWithDB(types.NewKVStoreKey("iavl2"), types.StoreTypeIAVL, nil)
	store.MountStoreWithDB(types.NewKVStoreKey("iavl3"), types.StoreTypeIAVL, nil)
	store.MountStoreWithDB(types.NewTransientStoreKey("trans1"), types.StoreTypeTransient, nil)
	if err := store.LoadLatestVersion(); err != nil {
		panic(err)
	}
	return store
}

func newMultiStoreWithMixedMountsAndBasicData(db dbm.DB) *rootmulti.Store {
	store := newMultiStoreWithMixedMounts(db)
	store1 := store.GetStoreByName("iavl1").(types.CommitKVStore)
	store2 := store.GetStoreByName("iavl2").(types.CommitKVStore)
	trans1 := store.GetStoreByName("trans1").(types.KVStore)

	store1.Set([]byte("a"), []byte{1})
	store1.Set([]byte("b"), []byte{1})
	store2.Set([]byte("X"), []byte{255})
	store2.Set([]byte("A"), []byte{101})
	trans1.Set([]byte("x1"), []byte{91})
	store.Commit()

	store1.Set([]byte("b"), []byte{2})
	store1.Set([]byte("c"), []byte{3})
	store2.Set([]byte("B"), []byte{102})
	store.Commit()

	store2.Set([]byte("C"), []byte{103})
	store2.Delete([]byte("X"))
	trans1.Set([]byte("x2"), []byte{92})
	store.Commit()

	return store
}

func assertStoresEqual(t *testing.T, expect, actual types.CommitKVStore, msgAndArgs ...interface{}) {
	t.Helper()
	assert.Equal(t, expect.LastCommitID(), actual.LastCommitID())
	expectIter := expect.Iterator(nil, nil)
	expectMap := map[string][]byte{}
	for ; expectIter.Valid(); expectIter.Next() {
		expectMap[string(expectIter.Key())] = expectIter.Value()
	}
	require.NoError(t, expectIter.Error())

	actualIter := expect.Iterator(nil, nil)
	actualMap := map[string][]byte{}
	for ; actualIter.Valid(); actualIter.Next() {
		actualMap[string(actualIter.Key())] = actualIter.Value()
	}
	require.NoError(t, actualIter.Error())

	assert.Equal(t, expectMap, actualMap, msgAndArgs...)
}

func TestMultistoreSnapshot_Checksum(t *testing.T) {
	// Chunks from different nodes must fit together, so all nodes must produce identical chunks.
	// This checksum test makes sure that the byte stream remains identical. If the test fails
	// without having changed the data (e.g. because the Protobuf or zlib encoding changes),
	// snapshottypes.CurrentFormat must be bumped.
	store := newMultiStoreWithGeneratedData(dbm.NewMemDB(), 5, 10000)
	version := uint64(store.LastCommitID().Version)

	testcases := []struct {
		format      uint32
		chunkHashes []string
	}{
		{1, []string{
			"503e5b51b657055b77e88169fadae543619368744ad15f1de0736c0a20482f24",
			"e1a0daaa738eeb43e778aefd2805e3dd720798288a410b06da4b8459c4d8f72e",
			"aa048b4ee0f484965d7b3b06822cf0772cdcaad02f3b1b9055e69f2cb365ef3c",
			"7921eaa3ed4921341e504d9308a9877986a879fe216a099c86e8db66fcba4c63",
			"a4a864e6c02c9fca5837ec80dc84f650b25276ed7e4820cf7516ced9f9901b86",
			"980925390cc50f14998ecb1e87de719ca9dd7e72f5fefbe445397bf670f36c31",
		}},
	}
	for _, tc := range testcases {
		tc := tc
		t.Run(fmt.Sprintf("Format %v", tc.format), func(t *testing.T) {
			ch := make(chan io.ReadCloser)
			go func() {
				streamWriter := snapshots.NewStreamWriter(ch)
				defer streamWriter.Close()
				require.NotNil(t, streamWriter)
				err := store.Snapshot(version, streamWriter)
				require.NoError(t, err)
			}()
			hashes := []string{}
			hasher := sha256.New()
			for chunk := range ch {
				hasher.Reset()
				_, err := io.Copy(hasher, chunk)
				require.NoError(t, err)
				hashes = append(hashes, hex.EncodeToString(hasher.Sum(nil)))
			}
			assert.Equal(t, tc.chunkHashes, hashes,
				"Snapshot output for format %v has changed", tc.format)
		})
	}
}

func TestMultistoreSnapshot_Errors(t *testing.T) {
	store := newMultiStoreWithMixedMountsAndBasicData(dbm.NewMemDB())

	testcases := map[string]struct {
		height     uint64
		expectType error
	}{
		"0 height":       {0, nil},
		"unknown height": {9, nil},
	}
	for name, tc := range testcases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			err := store.Snapshot(tc.height, nil)
			require.Error(t, err)
			if tc.expectType != nil {
				assert.True(t, errors.Is(err, tc.expectType))
			}
		})
	}
}

func TestMultistoreSnapshotRestore(t *testing.T) {
	source := newMultiStoreWithMixedMountsAndBasicData(dbm.NewMemDB())
	target := newMultiStoreWithMixedMounts(dbm.NewMemDB())
	version := uint64(source.LastCommitID().Version)
	require.EqualValues(t, 3, version)
	dummyExtensionItem := snapshottypes.SnapshotItem{
		Item: &snapshottypes.SnapshotItem_Extension{
			Extension: &snapshottypes.SnapshotExtensionMeta{
				Name:   "test",
				Format: 1,
			},
		},
	}

	chunks := make(chan io.ReadCloser, 100)
	go func() {
		streamWriter := snapshots.NewStreamWriter(chunks)
		require.NotNil(t, streamWriter)
		defer streamWriter.Close()
		err := source.Snapshot(version, streamWriter)
		require.NoError(t, err)
		// write an extension metadata
		err = streamWriter.WriteMsg(&dummyExtensionItem)
		require.NoError(t, err)
	}()

	streamReader, err := snapshots.NewStreamReader(chunks)
	require.NoError(t, err)
	nextItem, err := target.Restore(version, snapshottypes.CurrentFormat, streamReader)
	require.NoError(t, err)
	require.Equal(t, *dummyExtensionItem.GetExtension(), *nextItem.GetExtension())

	assert.Equal(t, source.LastCommitID(), target.LastCommitID())
	for _, key := range source.StoreKeysByName() {
		sourceStore := source.GetStoreByName(key.Name()).(types.CommitKVStore)
		targetStore := target.GetStoreByName(key.Name()).(types.CommitKVStore)
		switch sourceStore.GetStoreType() {
		case types.StoreTypeTransient:
			assert.False(t, targetStore.Iterator(nil, nil).Valid(),
				"transient store %v not empty", key.Name())
		default:
			assertStoresEqual(t, sourceStore, targetStore, "store %q not equal", key.Name())
		}
	}
}

func benchmarkMultistoreSnapshot(b *testing.B, stores uint8, storeKeys uint64) {
	b.Helper()
	b.Skip("Noisy with slow setup time, please see https://github.com/cosmos/cosmos-sdk/issues/8855.")

	b.ReportAllocs()
	b.StopTimer()
	source := newMultiStoreWithGeneratedData(dbm.NewMemDB(), stores, storeKeys)
	version := source.LastCommitID().Version
	require.EqualValues(b, 1, version)
	b.StartTimer()

	for i := 0; i < b.N; i++ {
		target := rootmulti.NewStore(dbm.NewMemDB(), log.NewNopLogger(), metrics.NewNoOpMetrics())
		for _, key := range source.StoreKeysByName() {
			target.MountStoreWithDB(key, types.StoreTypeIAVL, nil)
		}
		err := target.LoadLatestVersion()
		require.NoError(b, err)
		require.EqualValues(b, 0, target.LastCommitID().Version)

		chunks := make(chan io.ReadCloser)
		go func() {
			streamWriter := snapshots.NewStreamWriter(chunks)
			require.NotNil(b, streamWriter)
			err := source.Snapshot(uint64(version), streamWriter)
			require.NoError(b, err)
		}()
		for reader := range chunks {
			_, err := io.Copy(io.Discard, reader)
			require.NoError(b, err)
			err = reader.Close()
			require.NoError(b, err)
		}
	}
}

func benchmarkMultistoreSnapshotRestore(b *testing.B, stores uint8, storeKeys uint64) {
	b.Helper()
	b.Skip("Noisy with slow setup time, please see https://github.com/cosmos/cosmos-sdk/issues/8855.")

	b.ReportAllocs()
	b.StopTimer()
	source := newMultiStoreWithGeneratedData(dbm.NewMemDB(), stores, storeKeys)
	version := uint64(source.LastCommitID().Version)
	require.EqualValues(b, 1, version)
	b.StartTimer()

	for i := 0; i < b.N; i++ {
		target := rootmulti.NewStore(dbm.NewMemDB(), log.NewNopLogger(), metrics.NewNoOpMetrics())
		for _, key := range source.StoreKeysByName() {
			target.MountStoreWithDB(key, types.StoreTypeIAVL, nil)
		}
		err := target.LoadLatestVersion()
		require.NoError(b, err)
		require.EqualValues(b, 0, target.LastCommitID().Version)

		chunks := make(chan io.ReadCloser)
		go func() {
			writer := snapshots.NewStreamWriter(chunks)
			require.NotNil(b, writer)
			err := source.Snapshot(version, writer)
			require.NoError(b, err)
		}()
		reader, err := snapshots.NewStreamReader(chunks)
		require.NoError(b, err)
		_, err = target.Restore(version, snapshottypes.CurrentFormat, reader)
		require.NoError(b, err)
		require.Equal(b, source.LastCommitID(), target.LastCommitID())
	}
}

// signalingSnapshotter wraps a real Snapshotter and signals when Snapshot begins so
// tests can Close the manager mid-IAVL export.
type signalingSnapshotter struct {
	inner   snapshottypes.Snapshotter
	entered chan struct{}
	once    sync.Once
}

func (s *signalingSnapshotter) Snapshot(height uint64, protoWriter protoio.Writer) error {
	s.once.Do(func() { close(s.entered) })
	return s.inner.Snapshot(height, protoWriter)
}

func (s *signalingSnapshotter) PruneSnapshotHeight(height int64) {
	s.inner.PruneSnapshotHeight(height)
}

func (s *signalingSnapshotter) SetSnapshotInterval(snapshotInterval uint64) {
	s.inner.SetSnapshotInterval(snapshotInterval)
}

func (s *signalingSnapshotter) SetSnapshotAbortCh(abort <-chan struct{}) {
	type abortAware interface {
		SetSnapshotAbortCh(<-chan struct{})
	}
	if a, ok := s.inner.(abortAware); ok {
		a.SetSnapshotAbortCh(abort)
	}
}

func (s *signalingSnapshotter) Restore(height uint64, format uint32, protoReader protoio.Reader) (snapshottypes.SnapshotItem, error) {
	return s.inner.Restore(height, format, protoReader)
}

// TestMultistoreSnapshot_ManagerCloseAbortsExport starts a real rootmulti/IAVL
// snapshot via snapshots.Manager, then Close() mid-export. Create must return
// ErrAborted and must not leave the process panicking.
// See https://github.com/celestiaorg/celestia-app/issues/7252.
func TestMultistoreSnapshot_ManagerCloseAbortsExport(t *testing.T) {
	// Large enough that export outlives the test's Close after "entered".
	multiStore := newMultiStoreWithGeneratedData(dbm.NewMemDB(), 5, 10000)
	version := uint64(multiStore.LastCommitID().Version)
	require.EqualValues(t, 1, version)

	snapshotDB := dbm.NewMemDB()
	snapshotStore, err := snapshots.NewStore(snapshotDB, t.TempDir())
	require.NoError(t, err)

	gate := &signalingSnapshotter{
		inner:   multiStore,
		entered: make(chan struct{}),
	}
	opts := snapshottypes.NewSnapshotOptions(1, 1)
	manager := snapshots.NewManager(snapshotStore, opts, gate, nil, log.NewNopLogger())

	errCh := make(chan error, 1)
	go func() {
		_, err := manager.Create(version)
		errCh <- err
	}()

	select {
	case <-gate.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for rootmulti Snapshot to start")
	}

	require.NoError(t, manager.Close())

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, snapshots.ErrAborted)
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for Create to abort after Manager.Close")
	}

	// App state DB must remain usable; Manager.Close only closes snapshot metadata.
	require.EqualValues(t, 1, multiStore.LastCommitID().Version)
	store0 := multiStore.GetStoreByName("store0").(types.KVStore)
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, 0)
	require.NotNil(t, store0.Get(key))

	// Closed manager rejects new work.
	_, err = manager.Create(version)
	require.ErrorIs(t, err, snapshots.ErrAborted)
}

func BenchmarkMultistoreSnapshot100K(b *testing.B) {
	benchmarkMultistoreSnapshot(b, 10, 10000)
}

func BenchmarkMultistoreSnapshot1M(b *testing.B) {
	benchmarkMultistoreSnapshot(b, 10, 100000)
}

func BenchmarkMultistoreSnapshotRestore100K(b *testing.B) {
	benchmarkMultistoreSnapshotRestore(b, 10, 10000)
}

func BenchmarkMultistoreSnapshotRestore1M(b *testing.B) {
	benchmarkMultistoreSnapshotRestore(b, 10, 100000)
}
