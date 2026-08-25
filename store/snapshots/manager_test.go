package snapshots_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	db "github.com/cosmos/cosmos-db"
	protoio "github.com/cosmos/gogoproto/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store/snapshots"
	"cosmossdk.io/store/snapshots/types"
)

var opts = types.NewSnapshotOptions(1500, 2)

func TestManager_List(t *testing.T) {
	store := setupStore(t)
	snapshotter := &mockSnapshotter{}
	snapshotter.SetSnapshotInterval(opts.Interval)
	manager := snapshots.NewManager(store, opts, snapshotter, nil, log.NewNopLogger())
	require.Equal(t, opts.Interval, snapshotter.GetSnapshotInterval())

	mgrList, err := manager.List()
	require.NoError(t, err)
	storeList, err := store.List()
	require.NoError(t, err)

	require.NotEmpty(t, storeList)
	assert.Equal(t, storeList, mgrList)

	// list should not block or error on busy managers
	manager = setupBusyManager(t)
	list, err := manager.List()
	require.NoError(t, err)
	assert.Equal(t, []*types.Snapshot{}, list)
}

func TestManager_LoadChunk(t *testing.T) {
	store := setupStore(t)
	manager := snapshots.NewManager(store, opts, &mockSnapshotter{}, nil, log.NewNopLogger())

	// Existing chunk should return body
	chunk, err := manager.LoadChunk(2, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, []byte{2, 1, 1}, chunk)

	// Missing chunk should return nil
	chunk, err = manager.LoadChunk(2, 1, 9)
	require.NoError(t, err)
	assert.Nil(t, chunk)

	// LoadChunk should not block or error on busy managers
	manager = setupBusyManager(t)
	chunk, err = manager.LoadChunk(2, 1, 0)
	require.NoError(t, err)
	assert.Nil(t, chunk)
}

func TestManager_Take(t *testing.T) {
	store := setupStore(t)
	items := [][]byte{
		{1, 2, 3},
		{4, 5, 6},
		{7, 8, 9},
	}
	snapshotter := &mockSnapshotter{
		items:         items,
		prunedHeights: make(map[int64]struct{}),
	}
	extSnapshotter := newExtSnapshotter(10)

	expectChunks := snapshotItems(items, extSnapshotter)
	manager := snapshots.NewManager(store, opts, snapshotter, nil, log.NewNopLogger())
	err := manager.RegisterExtensions(extSnapshotter)
	require.NoError(t, err)

	// nil manager should return error
	_, err = (*snapshots.Manager)(nil).Create(1)
	require.Error(t, err)

	// creating a snapshot at a lower height than the latest should error
	_, err = manager.Create(3)
	require.Error(t, err)
	_, didPruneHeight := snapshotter.prunedHeights[3]
	require.True(t, didPruneHeight)

	// creating a snapshot at a higher height should be fine, and should return it
	snapshot, err := manager.Create(5)
	require.NoError(t, err)
	_, didPruneHeight = snapshotter.prunedHeights[5]
	require.True(t, didPruneHeight)

	assert.Equal(t, &types.Snapshot{
		Height: 5,
		Format: snapshotter.SnapshotFormat(),
		Chunks: 1,
		Hash:   []uint8{0xc5, 0xf7, 0xfe, 0xea, 0xd3, 0x4d, 0x3e, 0x87, 0xff, 0x41, 0xa2, 0x27, 0xfa, 0xcb, 0x38, 0x17, 0xa, 0x5, 0xeb, 0x27, 0x4e, 0x16, 0x5e, 0xf3, 0xb2, 0x8b, 0x47, 0xd1, 0xe6, 0x94, 0x7e, 0x8b},
		Metadata: types.Metadata{
			ChunkHashes: checksums(expectChunks),
		},
	}, snapshot)

	storeSnapshot, chunks, err := store.Load(snapshot.Height, snapshot.Format)
	require.NoError(t, err)
	assert.Equal(t, snapshot, storeSnapshot)
	assert.Equal(t, expectChunks, readChunks(chunks))

	// creating a snapshot while a different snapshot is being created should error
	manager = setupBusyManager(t)
	_, err = manager.Create(9)
	require.Error(t, err)
}

func TestManager_Prune(t *testing.T) {
	store := setupStore(t)
	snapshotter := &mockSnapshotter{}
	snapshotter.SetSnapshotInterval(opts.Interval)
	manager := snapshots.NewManager(store, opts, snapshotter, nil, log.NewNopLogger())

	pruned, err := manager.Prune(2)
	require.NoError(t, err)
	assert.EqualValues(t, 1, pruned)

	list, err := manager.List()
	require.NoError(t, err)
	assert.Len(t, list, 3)

	// Prune should error while a snapshot is being taken
	manager = setupBusyManager(t)
	_, err = manager.Prune(2)
	require.Error(t, err)
}

func TestManager_Restore(t *testing.T) {
	store := setupStore(t)
	target := &mockSnapshotter{
		prunedHeights: make(map[int64]struct{}),
	}
	extSnapshotter := newExtSnapshotter(0)
	manager := snapshots.NewManager(store, opts, target, nil, log.NewNopLogger())
	err := manager.RegisterExtensions(extSnapshotter)
	require.NoError(t, err)

	expectItems := [][]byte{
		{1, 2, 3},
		{4, 5, 6},
		{7, 8, 9},
	}

	chunks := snapshotItems(expectItems, newExtSnapshotter(10))

	// Restore errors on invalid format
	err = manager.Restore(types.Snapshot{
		Height:   3,
		Format:   0,
		Hash:     []byte{1, 2, 3},
		Chunks:   uint32(len(chunks)),
		Metadata: types.Metadata{ChunkHashes: checksums(chunks)},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrUnknownFormat)

	// Restore errors on no chunks
	err = manager.Restore(types.Snapshot{Height: 3, Format: types.CurrentFormat, Hash: []byte{1, 2, 3}})
	require.Error(t, err)

	// Restore errors on chunk and chunkhashes mismatch
	err = manager.Restore(types.Snapshot{
		Height:   3,
		Format:   types.CurrentFormat,
		Hash:     []byte{1, 2, 3},
		Chunks:   4,
		Metadata: types.Metadata{ChunkHashes: checksums(chunks)},
	})
	require.Error(t, err)

	// Starting a restore works
	err = manager.Restore(types.Snapshot{
		Height:   3,
		Format:   types.CurrentFormat,
		Hash:     []byte{1, 2, 3},
		Chunks:   1,
		Metadata: types.Metadata{ChunkHashes: checksums(chunks)},
	})
	require.NoError(t, err)

	// While the restore is in progress, any other operations fail
	_, err = manager.Create(4)
	require.Error(t, err)
	_, didPruneHeight := target.prunedHeights[4]
	require.True(t, didPruneHeight)

	_, err = manager.Prune(1)
	require.Error(t, err)

	// Feeding an invalid chunk should error due to invalid checksum, but not abort restoration.
	_, err = manager.RestoreChunk([]byte{9, 9, 9})
	require.Error(t, err)
	require.True(t, errors.Is(err, types.ErrChunkHashMismatch))

	// Feeding the chunks should work
	for i, chunk := range chunks {
		done, err := manager.RestoreChunk(chunk)
		require.NoError(t, err)
		if i == len(chunks)-1 {
			assert.True(t, done)
		} else {
			assert.False(t, done)
		}
	}

	assert.Equal(t, expectItems, target.items)
	assert.Equal(t, 10, len(extSnapshotter.state))

	// The snapshot is saved in local snapshot store
	snapshots, err := store.List()
	require.NoError(t, err)
	snapshot := snapshots[0]
	require.Equal(t, uint64(3), snapshot.Height)
	require.Equal(t, types.CurrentFormat, snapshot.Format)

	// Starting a new restore should fail now, because the target already has contents.
	err = manager.Restore(types.Snapshot{
		Height:   3,
		Format:   types.CurrentFormat,
		Hash:     []byte{1, 2, 3},
		Chunks:   3,
		Metadata: types.Metadata{ChunkHashes: checksums(chunks)},
	})
	require.Error(t, err)

	// But if we clear out the target we should be able to start a new restore. This time we'll
	// fail it with a checksum error. That error should stop the operation, so that we can do
	// a prune operation right after.
	target.items = nil
	err = manager.Restore(types.Snapshot{
		Height:   3,
		Format:   types.CurrentFormat,
		Hash:     []byte{1, 2, 3},
		Chunks:   1,
		Metadata: types.Metadata{ChunkHashes: checksums(chunks)},
	})
	require.NoError(t, err)
}

func TestManager_TakeError(t *testing.T) {
	snapshotter := &mockErrorSnapshotter{}
	store, err := snapshots.NewStore(db.NewMemDB(), GetTempDir(t))
	require.NoError(t, err)
	manager := snapshots.NewManager(store, opts, snapshotter, nil, log.NewNopLogger())

	_, err = manager.Create(1)
	require.Error(t, err)
}

func TestManager_CloseIdempotent(t *testing.T) {
	store, err := snapshots.NewStore(db.NewMemDB(), t.TempDir())
	require.NoError(t, err)
	manager := snapshots.NewManager(store, opts, &mockSnapshotter{}, nil, log.NewNopLogger())
	require.NoError(t, manager.Close())
	require.NoError(t, manager.Close())
}

// TestManager_CloseAbortsPreWriteBlockingSnapshotter covers shutdown while Snapshot
// is blocked before any WriteMsg. The snapshotter observes abortCh (injected by
// NewManager) and returns, so Close does not hang.
func TestManager_CloseAbortsPreWriteBlockingSnapshotter(t *testing.T) {
	store, err := snapshots.NewStore(db.NewMemDB(), t.TempDir())
	require.NoError(t, err)

	blocker := &preWriteBlockingSnapshotter{entered: make(chan struct{})}
	manager := snapshots.NewManager(store, opts, blocker, nil, log.NewNopLogger())
	require.NotNil(t, blocker.abort, "NewManager must inject abort channel")

	createErr := make(chan error, 1)
	go func() {
		_, err := manager.Create(1)
		createErr <- err
	}()

	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Snapshot to block before WriteMsg")
	}

	require.NoError(t, manager.Close())

	select {
	case err := <-createErr:
		require.ErrorIs(t, err, snapshots.ErrAborted)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Create to abort — pre-write block did not observe abortCh")
	}
}

// preWriteBlockingSnapshotter blocks before WriteMsg until the manager abort channel
// is closed (SetSnapshotAbortCh).
type preWriteBlockingSnapshotter struct {
	entered chan struct{}
	abort   <-chan struct{}
}

func (p *preWriteBlockingSnapshotter) SetSnapshotAbortCh(abort <-chan struct{}) {
	p.abort = abort
}

func (p *preWriteBlockingSnapshotter) Snapshot(height uint64, protoWriter protoio.Writer) error {
	close(p.entered)
	<-p.abort
	return snapshots.ErrAborted
}

func (p *preWriteBlockingSnapshotter) PruneSnapshotHeight(height int64)            {}
func (p *preWriteBlockingSnapshotter) SetSnapshotInterval(snapshotInterval uint64) {}
func (p *preWriteBlockingSnapshotter) Restore(height uint64, format uint32, protoReader protoio.Reader) (types.SnapshotItem, error) {
	panic("not implemented")
}

// TestManager_ConcurrentCloseWaitsForCompletion ensures a second Close does not
// return while the first is still waiting for in-flight snapshot work. Both
// callers share completion and receive the same result.
func TestManager_ConcurrentCloseWaitsForCompletion(t *testing.T) {
	store, err := snapshots.NewStore(db.NewMemDB(), t.TempDir())
	require.NoError(t, err)

	gate := &gatedSnapshotter{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	manager := snapshots.NewManager(store, opts, gate, nil, log.NewNopLogger())

	createErr := make(chan error, 1)
	go func() {
		_, err := manager.Create(1)
		createErr <- err
	}()

	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Snapshot to start")
	}

	closeErr1 := make(chan error, 1)
	closeErr2 := make(chan error, 1)
	go func() { closeErr1 <- manager.Close() }()
	go func() { closeErr2 <- manager.Close() }()

	// While Snapshot is still blocked, neither Close may return.
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-closeErr1:
		t.Fatalf("first Close returned early while snapshot still running: %v", err)
	default:
	}
	select {
	case err := <-closeErr2:
		t.Fatalf("second Close returned early while snapshot still running: %v", err)
	default:
	}

	close(gate.release)

	select {
	case err := <-closeErr1:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first Close")
	}
	select {
	case err := <-closeErr2:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for second Close")
	}

	select {
	case err := <-createErr:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Create after Close")
	}
}

// gatedSnapshotter blocks in Snapshot until release is closed, without WriteMsg.
// Used to hold an opSnapshot open while testing concurrent Close.
type gatedSnapshotter struct {
	entered chan struct{}
	release chan struct{}
}

func (g *gatedSnapshotter) Snapshot(height uint64, protoWriter protoio.Writer) error {
	close(g.entered)
	<-g.release
	return snapshots.ErrAborted
}
func (g *gatedSnapshotter) PruneSnapshotHeight(height int64)            {}
func (g *gatedSnapshotter) SetSnapshotInterval(snapshotInterval uint64) {}
func (g *gatedSnapshotter) Restore(height uint64, format uint32, protoReader protoio.Reader) (types.SnapshotItem, error) {
	panic("not implemented")
}

// TestManager_CloseAfterRestoreSetupFailure ensures Restore rolls back opRestore when
// snapshot directory creation fails, so Close does not hang waiting for opNone.
func TestManager_CloseAfterRestoreSetupFailure(t *testing.T) {
	dir := t.TempDir()
	store, err := snapshots.NewStore(db.NewMemDB(), dir)
	require.NoError(t, err)
	manager := snapshots.NewManager(store, opts, &mockSnapshotter{prunedHeights: map[int64]struct{}{}}, nil, log.NewNopLogger())

	// pathSnapshot is dir/<height>/<format>; place a file at dir/<height> so MkdirAll fails.
	height := uint64(3)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "3"), []byte("not-a-directory"), 0o600))

	err = manager.Restore(types.Snapshot{
		Height:   height,
		Format:   types.CurrentFormat,
		Hash:     []byte{1},
		Chunks:   1,
		Metadata: types.Metadata{ChunkHashes: [][]byte{{1}}},
	})
	require.Error(t, err)

	// A subsequent operation must be allowed (opRestore was rolled back).
	_, err = manager.Prune(1)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		done <- manager.Close()
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung after Restore setup failure — opRestore likely not rolled back")
	}
}

// writingSnapshotter keeps writing until the stream writer fails (e.g. aborted by Close).
type writingSnapshotter struct {
	started       chan struct{}
	once          sync.Once
	prunedHeights map[int64]struct{}
}

func (w *writingSnapshotter) Snapshot(height uint64, protoWriter protoio.Writer) error {
	w.once.Do(func() { close(w.started) })
	for {
		err := protoWriter.WriteMsg(&types.SnapshotItem{
			Item: &types.SnapshotItem_Store{
				Store: &types.SnapshotStoreItem{Name: "busy"},
			},
		})
		if err != nil {
			return err
		}
	}
}

func (w *writingSnapshotter) PruneSnapshotHeight(height int64) {
	if w.prunedHeights == nil {
		w.prunedHeights = make(map[int64]struct{})
	}
	w.prunedHeights[height] = struct{}{}
}
func (w *writingSnapshotter) SetSnapshotInterval(snapshotInterval uint64) {}
func (w *writingSnapshotter) Restore(height uint64, format uint32, protoReader protoio.Reader) (types.SnapshotItem, error) {
	panic("not implemented")
}

func TestManager_CloseAbortsWritingSnapshot(t *testing.T) {
	store, err := snapshots.NewStore(db.NewMemDB(), t.TempDir())
	require.NoError(t, err)

	writer := &writingSnapshotter{
		started:       make(chan struct{}),
		prunedHeights: make(map[int64]struct{}),
	}
	manager := snapshots.NewManager(store, opts, writer, nil, log.NewNopLogger())

	errCh := make(chan error, 1)
	go func() {
		_, err := manager.Create(1)
		errCh <- err
	}()

	select {
	case <-writer.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for writing snapshot to start")
	}

	require.NoError(t, manager.Close())

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, snapshots.ErrAborted)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Create to abort")
	}

	_, didPruneHeight := writer.prunedHeights[1]
	require.False(t, didPruneHeight, "aborted snapshots should not prune snapshot heights")

	// Further snapshot attempts should fail fast.
	_, err = manager.Create(2)
	require.ErrorIs(t, err, snapshots.ErrAborted)

	manager.SnapshotIfApplicable(int64(opts.Interval)) // must not panic or hang
}
