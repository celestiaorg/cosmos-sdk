package baseapp

// Parallel recheck: groups of txs run concurrently on branches of checkState that track every key
// read and written. Merge is allowed only once Validate proved no group touched a key another group
// wrote, so the result equals the sequential recheck; otherwise the caller reruns sequentially.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"

	abci "github.com/cometbft/cometbft/abci/types"
	dbm "github.com/cosmos/cosmos-db"

	"cosmossdk.io/store/cachekv"
	"cosmossdk.io/store/cachemulti"
	"cosmossdk.io/store/dbadapter"
	"cosmossdk.io/store/tracekv"
	storetypes "cosmossdk.io/store/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/mempool"
)

// LockCheckState takes the lock CheckTx takes, for a caller that runs a batch
// of rechecks with RecheckExecutor or CheckTxUnlocked. It returns the unlock.
func (app *BaseApp) LockCheckState() (unlock func()) {
	app.checkStateMu.Lock()
	return app.checkStateMu.Unlock
}

// CheckTxUnlocked is CheckTx for a caller that holds LockCheckState. tx, if
// not nil, is the decode of req.Tx.
func (app *BaseApp) CheckTxUnlocked(req *abci.RequestCheckTx, tx sdk.Tx) (*abci.ResponseCheckTx, error) {
	var mode execMode
	switch req.Type {
	case abci.CheckTxType_New:
		mode = execModeCheck
	case abci.CheckTxType_Recheck:
		mode = execModeReCheck
	default:
		return nil, fmt.Errorf("unknown RequestCheckTx type: %s", req.Type)
	}
	p, err := app.runTxAnte(mode, req.Tx, tx)
	return app.checkTxResponse(app.finishTx(mode, p, err)), nil
}

func (app *BaseApp) checkTxResponse(gInfo sdk.GasInfo, result *sdk.Result, anteEvents []abci.Event, priority int64, err error) *abci.ResponseCheckTx {
	if err != nil {
		return sdkerrors.ResponseCheckTxWithEvents(err, gInfo.GasWanted, gInfo.GasUsed, anteEvents, app.trace)
	}
	return &abci.ResponseCheckTx{
		GasWanted: int64(gInfo.GasWanted), // TODO: Should type accept unsigned ints?
		GasUsed:   int64(gInfo.GasUsed),   // TODO: Should type accept unsigned ints?
		Log:       result.Log,
		Data:      result.Data,
		Events:    sdk.MarkEventsToIndex(result.Events, app.indexEvents),
		Priority:  priority,
	}
}

// ErrRecheckConflict reports that the groups of a parallel recheck were not
// independent; the caller must discard the run and recheck sequentially.
var ErrRecheckConflict = errors.New("parallel recheck: groups not independent")

// RecheckExecutor runs groups of rechecks on isolated branches of checkState.
// The caller holds LockCheckState from NewRecheckExecutor until it calls Merge
// or drops the executor.
type RecheckExecutor struct {
	app        *BaseApp
	baseCtx    sdk.Context
	keys       map[string]storetypes.StoreKey
	base       map[storetypes.StoreKey]storetypes.KVStore
	isAdditive func(storetypes.StoreKey, []byte) bool
	// fresh: checkState equals the committed state, so branches read the committed stores
	// directly instead of through checkState's locked cache
	fresh bool

	groups []recheckGroup // indexed by group id
}

// recheckGroup is a group's footprint in each store.
type recheckGroup map[storetypes.StoreKey]*footprint

type footprint struct {
	reads  map[string]struct{}
	writes map[string]kvWrite // non-additive keys
	adds   map[string]kvWrite // additive keys
	ranges [][2][]byte
	unsafe bool
}

type kvWrite struct {
	value   []byte
	deleted bool
}

func (v kvWrite) get() []byte {
	if v.deleted {
		return nil
	}
	return v.value
}

// NewRecheckExecutor prepares a parallel recheck of numGroups groups, or returns an error if this
// app cannot run one. isAdditive, if not nil, names keys every group reads at their base value; the
// caller merges them (see Merge) and accounts for the gas their shorter values save.
func (app *BaseApp) NewRecheckExecutor(numGroups int, isAdditive func(storetypes.StoreKey, []byte) bool) (*RecheckExecutor, error) {
	if app.checkState == nil {
		return nil, errors.New("checkState not set")
	}
	if _, ok := app.mempool.(mempool.NoOpMempool); !ok && app.mempool != nil {
		// a failed recheck removes the tx from the app-side mempool, which is not goroutine-safe
		return nil, errors.New("parallel recheck needs the no-op app mempool")
	}
	if app.postHandler != nil {
		return nil, errors.New("parallel recheck does not support post handlers")
	}
	kn, ok := app.cms.(interface {
		StoreKeysByName() map[string]storetypes.StoreKey
	})
	if !ok {
		return nil, fmt.Errorf("commit multistore %T does not list its keys", app.cms)
	}
	e := &RecheckExecutor{
		app:        app,
		baseCtx:    app.checkState.Context(),
		keys:       kn.StoreKeysByName(),
		base:       make(map[storetypes.StoreKey]storetypes.KVStore),
		isAdditive: isAdditive,
		// a PrepareCheckStater writes checkState right after Commit
		fresh:  !app.checkStateDirty && app.prepareCheckStater == nil,
		groups: make([]recheckGroup, numGroups),
	}
	src := storetypes.MultiStore(app.checkState.ms)
	if e.fresh {
		src = app.cms
	}
	for _, k := range e.keys {
		e.base[k] = src.GetKVStore(k)
	}
	return e, nil
}

// Fresh reports whether branches read the committed stores directly (no
// checkState write since Commit).
func (e *RecheckExecutor) Fresh() bool { return e.fresh }

func (e *RecheckExecutor) additive(k storetypes.StoreKey, key []byte) bool {
	return e.isAdditive != nil && e.isAdditive(k, key)
}

// RecheckWorker runs groups one after another on one goroutine.
type RecheckWorker struct {
	e      *RecheckExecutor
	tracks map[storetypes.StoreKey]*trackStore
	cur    recheckGroup
	ctx    sdk.Context
}

// NewWorker returns a worker. Each worker must be used by one goroutine only.
func (e *RecheckExecutor) NewWorker() *RecheckWorker {
	w := &RecheckWorker{e: e, tracks: make(map[storetypes.StoreKey]*trackStore, len(e.keys))}
	stores := make(map[storetypes.StoreKey]storetypes.CacheWrapper, len(e.keys))
	for _, k := range e.keys {
		t := &trackStore{w: w, key: k, base: e.base[k], cache: make(map[string][]byte)}
		w.tracks[k] = t
		stores[k] = t
	}
	ms := &workerMS{w: w, stores: stores, db: dbadapter.Store{DB: dbm.NewMemDB()}}
	w.ctx = e.baseCtx.WithMultiStore(ms).WithEventManager(sdk.NewEventManager())
	return w
}

// workerMS is a worker's view of checkState: txs branch it and write back into
// its track stores, which attribute every access to the current group.
type workerMS struct {
	w      *RecheckWorker
	stores map[storetypes.StoreKey]storetypes.CacheWrapper
	db     dbadapter.Store
}

var _ storetypes.CacheMultiStore = (*workerMS)(nil)

func (m *workerMS) GetStoreType() storetypes.StoreType { return storetypes.StoreTypeMulti }
func (m *workerMS) CacheWrap() storetypes.CacheWrap {
	return m.CacheMultiStore().(storetypes.CacheWrap)
}

func (m *workerMS) CacheWrapWithTrace(io.Writer, storetypes.TraceContext) storetypes.CacheWrap {
	return m.CacheWrap()
}

func (m *workerMS) CacheMultiStore() storetypes.CacheMultiStore {
	return cachemulti.NewFromKVStore(m.db, m.stores, m.w.e.keys, nil, nil)
}

func (m *workerMS) CacheMultiStoreWithVersion(int64) (storetypes.CacheMultiStore, error) {
	return nil, errors.New("parallel recheck branch cannot load versions")
}
func (m *workerMS) GetStore(k storetypes.StoreKey) storetypes.Store { return m.GetKVStore(k) }
func (m *workerMS) GetKVStore(k storetypes.StoreKey) storetypes.KVStore {
	t := m.w.tracks[k]
	if t == nil {
		panic(fmt.Sprintf("kv store with key %v has not been registered in stores", k))
	}
	return t
}
func (m *workerMS) TracingEnabled() bool                                            { return false }
func (m *workerMS) SetTracer(io.Writer) storetypes.MultiStore                       { return m }
func (m *workerMS) SetTracingContext(storetypes.TraceContext) storetypes.MultiStore { return m }
func (m *workerMS) LatestVersion() int64                                            { return m.w.e.app.LastBlockHeight() }
func (m *workerMS) Write()                                                          {}

// BeginGroup starts group g on this worker.
func (w *RecheckWorker) BeginGroup(g int) {
	w.cur = make(recheckGroup)
	w.e.groups[g] = w.cur
}

// EndGroup ends the current group.
func (w *RecheckWorker) EndGroup() { w.cur = nil }

// Context is the check context on this worker's branch, for app-level reads.
func (w *RecheckWorker) Context() sdk.Context { return w.ctx }

// RecheckTx rechecks one tx of the current group on the branch, as
// CheckTx(Type=Recheck) does on checkState. tx, if not nil, is the decode of txBytes.
func (w *RecheckWorker) RecheckTx(txBytes []byte, tx sdk.Tx) *abci.ResponseCheckTx {
	app := w.e.app
	ctx := app.txContext(w.ctx, execModeReCheck, txBytes)
	p, err := app.runTxAnteCtx(execModeReCheck, ctx, txBytes, tx)
	return app.checkTxResponse(app.finishTx(execModeReCheck, p, err))
}

func (g recheckGroup) fp(k storetypes.StoreKey) *footprint {
	f := g[k]
	if f == nil {
		f = &footprint{reads: make(map[string]struct{}), writes: make(map[string]kvWrite), adds: make(map[string]kvWrite)}
		g[k] = f
	}
	return f
}

// trackStore is a worker's view of one store: the base state (immutable while
// workers run, so cached) under the current group's writes. It records each
// group's reads and writes.
type trackStore struct {
	w     *RecheckWorker
	key   storetypes.StoreKey
	base  storetypes.KVStore
	cache map[string][]byte // base values read so far
}

var _ storetypes.KVStore = (*trackStore)(nil)

func (t *trackStore) baseGet(key []byte) []byte {
	v, ok := t.cache[string(key)]
	if !ok {
		v = t.base.Get(key)
		t.cache[string(key)] = v
	}
	return v
}

// Get serves the current group's writes, then the base. A key another group of
// this worker wrote reads its base value, which Validate then rejects.
func (t *trackStore) Get(key []byte) []byte {
	storetypes.AssertValidKey(key)
	if g := t.w.cur; g != nil {
		f := g.fp(t.key)
		writes := f.writes
		if t.w.e.additive(t.key, key) {
			writes = f.adds
		} else {
			f.reads[string(key)] = struct{}{}
		}
		if v, ok := writes[string(key)]; ok {
			return v.get()
		}
	}
	return t.baseGet(key)
}

func (t *trackStore) Has(key []byte) bool { return t.Get(key) != nil }

func (t *trackStore) record(key []byte, v kvWrite) {
	g := t.w.cur
	if g == nil {
		panic("parallel recheck: write outside a group")
	}
	f := g.fp(t.key)
	if t.w.e.additive(t.key, key) {
		f.adds[string(key)] = v
	} else {
		f.writes[string(key)] = v
	}
}

func (t *trackStore) Set(key, value []byte) {
	storetypes.AssertValidKey(key)
	storetypes.AssertValidValue(value)
	t.record(key, kvWrite{value: value})
}

func (t *trackStore) Delete(key []byte) { t.record(key, kvWrite{deleted: true}) }

func (t *trackStore) iterator(start, end []byte, ascending bool) storetypes.Iterator {
	if g := t.w.cur; g != nil {
		f := g.fp(t.key)
		f.ranges = append(f.ranges, [2][]byte{bytes.Clone(start), bytes.Clone(end)})
		// The base iterator misses the group's own writes, and checkState's cache
		// iterator is not goroutine-safe: discard such a run, iterating nothing.
		if len(f.writes) > 0 || len(f.adds) > 0 || !t.w.e.fresh {
			f.unsafe = true
			return dbadapter.Store{DB: dbm.NewMemDB()}.Iterator(start, end)
		}
	}
	if ascending {
		return t.base.Iterator(start, end)
	}
	return t.base.ReverseIterator(start, end)
}

func (t *trackStore) Iterator(start, end []byte) storetypes.Iterator {
	return t.iterator(start, end, true)
}

func (t *trackStore) ReverseIterator(start, end []byte) storetypes.Iterator {
	return t.iterator(start, end, false)
}

func (t *trackStore) GetStoreType() storetypes.StoreType { return t.base.GetStoreType() }

func (t *trackStore) CacheWrap() storetypes.CacheWrap { return cachekv.NewStore(t) }

func (t *trackStore) CacheWrapWithTrace(w io.Writer, tc storetypes.TraceContext) storetypes.CacheWrap {
	return cachekv.NewStore(tracekv.NewStore(t, w, tc))
}

// Validate returns ErrRecheckConflict, wrapped with the first conflict found,
// unless no group read, iterated over or differently wrote a non-additive key
// another group wrote. Reads are checked by parallelism goroutines.
func (e *RecheckExecutor) Validate(parallelism int) error {
	type wkey struct {
		store storetypes.StoreKey
		key   string
	}
	type writer struct {
		group int
		val   kvWrite
		multi bool // written by >1 group (with equal values, unless additive)
	}
	writers := make(map[wkey]*writer)
	for gi, g := range e.groups {
		for k, f := range g {
			if f.unsafe {
				return fmt.Errorf("%w: group %d iterated store %s", ErrRecheckConflict, gi, k.Name())
			}
			for key, v := range f.writes {
				wk := wkey{k, key}
				if w, ok := writers[wk]; ok {
					if w.val.deleted != v.deleted || !bytes.Equal(w.val.value, v.value) {
						return fmt.Errorf("%w: groups %d and %d wrote %s/%X", ErrRecheckConflict, w.group, gi, k.Name(), key)
					}
					w.multi = true
					continue
				}
				writers[wk] = &writer{group: gi, val: v}
			}
			// groups never read additive keys, but may iterate over them
			for key := range f.adds {
				if w, ok := writers[wkey{k, key}]; ok {
					w.multi = true
				} else {
					writers[wkey{k, key}] = &writer{group: gi}
				}
			}
		}
	}
	if len(writers) == 0 {
		return nil
	}
	check := func(gi int) error {
		for k, f := range e.groups[gi] {
			for key := range f.reads {
				w, ok := writers[wkey{k, key}]
				if ok && (w.group != gi || w.multi) {
					return fmt.Errorf("%w: group %d read %s/%X written by group %d", ErrRecheckConflict, gi, k.Name(), []byte(key), w.group)
				}
			}
			for _, r := range f.ranges {
				for wk, w := range writers {
					if wk.store != k || (w.group == gi && !w.multi) {
						continue
					}
					kb := []byte(wk.key)
					if (r[0] == nil || bytes.Compare(kb, r[0]) >= 0) && (r[1] == nil || bytes.Compare(kb, r[1]) < 0) {
						return fmt.Errorf("%w: group %d iterated over %s/%X written by group %d", ErrRecheckConflict, gi, k.Name(), kb, w.group)
					}
				}
			}
		}
		return nil
	}
	parallelism = max(1, min(parallelism, len(e.groups)))
	errs := make([]error, parallelism)
	var wg sync.WaitGroup
	for sh := range parallelism {
		wg.Go(func() {
			for gi := sh; gi < len(e.groups); gi += parallelism {
				if err := check(gi); err != nil {
					errs[sh] = err
					return
				}
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// AdditiveWrite describes one additive key: the base value every group started
// from, and the final value (nil if deleted) of each group that wrote it.
type AdditiveWrite struct {
	Base   []byte
	Groups []int
	Finals [][]byte
}

// AdditiveWrites returns the additive keys some group wrote, in group order.
func (e *RecheckExecutor) AdditiveWrites() map[storetypes.StoreKey]map[string]AdditiveWrite {
	out := make(map[storetypes.StoreKey]map[string]AdditiveWrite)
	for gi, g := range e.groups {
		for k, f := range g {
			for key, v := range f.adds {
				m := out[k]
				if m == nil {
					m = make(map[string]AdditiveWrite)
					out[k] = m
				}
				aw, ok := m[key]
				if !ok {
					aw.Base = e.base[k].Get([]byte(key))
				}
				aw.Groups = append(aw.Groups, gi)
				aw.Finals = append(aw.Finals, v.get())
				m[key] = aw
			}
		}
	}
	return out
}

// Merge writes the groups' writes into checkState, with merged giving the value (nil deletes it) of
// every key AdditiveWrites returned; other additive writes are dropped. Call it only after Validate
// returned nil.
func (e *RecheckExecutor) Merge(merged map[storetypes.StoreKey]map[string][]byte) {
	app := e.app
	// later executors at this height must read through checkState
	app.checkStateDirty = true
	// Each store has its own cache, and no two groups wrote one key with
	// different values, so stores merge concurrently in any order.
	var wg sync.WaitGroup
	for _, k := range e.keys {
		wg.Go(func() {
			var dst storetypes.KVStore
			apply := func(key string, v kvWrite) {
				if dst == nil {
					dst = app.checkState.ms.GetKVStore(k)
				}
				if v.deleted {
					dst.Delete([]byte(key))
				} else {
					dst.Set([]byte(key), v.value)
				}
			}
			for _, g := range e.groups {
				for key, v := range g[k].writesOrNil() {
					apply(key, v)
				}
			}
			for key, v := range merged[k] {
				apply(key, kvWrite{value: v, deleted: v == nil})
			}
		})
	}
	wg.Wait()
}

func (f *footprint) writesOrNil() map[string]kvWrite {
	if f == nil {
		return nil
	}
	return f.writes
}
