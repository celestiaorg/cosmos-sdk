// Package applier replays recorded changesets into a rootmulti store and
// verifies the resulting app hash at every height.
package applier

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tendermint/tendermint/libs/log"
	dbm "github.com/tendermint/tm-db"

	pruningtypes "github.com/cosmos/cosmos-sdk/pruning/types"
	"github.com/cosmos/cosmos-sdk/store/cache"
	"github.com/cosmos/cosmos-sdk/store/changeset"
	"github.com/cosmos/cosmos-sdk/store/iavl"
	"github.com/cosmos/cosmos-sdk/store/rootmulti"
	"github.com/cosmos/cosmos-sdk/store/types"
)

// Options configure the applier's multistore.
type Options struct {
	Pruning         pruningtypes.PruningOptions // default: everything (as the sync nodes)
	IAVLCacheSize   int                         // default iavl.DefaultIAVLCacheSize
	DisableFastNode bool                        // forced on by deferred persistence anyway
	// InterBlockCache mirrors the node default. It is required for exact
	// upgrade semantics: baseapp reloads the stores mid-Commit at an app
	// version change and only the cache keeps the pending working trees.
	InterBlockCache bool
	// Parallel applies the ops of different stores concurrently (independent trees).
	Parallel bool
	Logger   log.Logger
}

// DefaultOptions returns the options used by csapply.
func DefaultOptions() Options {
	return Options{
		Pruning:         pruningtypes.NewPruningOptions(pruningtypes.PruningEverything),
		IAVLCacheSize:   iavl.DefaultIAVLCacheSize,
		DisableFastNode: true,
		InterBlockCache: true,
	}
}

// Applier owns a rootmulti store opened on a node's application DB.
type Applier struct {
	RS   *rootmulti.Store
	opts Options
	keys map[string]types.StoreKey
	// Expected, if set, returns the trusted app hash for a height (e.g. the
	// AppHash of header h+1). ok=false means "unknown": only the recorded hash
	// is checked (and reported as such).
	Expected func(height int64) (hash []byte, ok bool, err error)

	Latest       int64
	Applied      int64
	Ops          int64
	CheckedExt   int64 // heights verified against Expected
	CheckedLocal int64 // heights verified against the recorded hash only
	Unverified   int64 // heights with neither a trusted nor a recorded hash (synthetic input)
	// HandoverAt is the first block recorded by the SDK v0.50 store (app v4+,
	// FlagStoreV1) that Run met; it stopped before it. Continue with cstool50.
	HandoverAt int64

	OpsTime, CommitTime time.Duration
}

// Open mounts the stores of manifest on db and loads the latest version.
func Open(db dbm.DB, manifest []changeset.StoreDecl, opts Options) (*Applier, error) {
	if opts.Logger == nil {
		opts.Logger = log.NewNopLogger()
	}
	if opts.IAVLCacheSize == 0 {
		opts.IAVLCacheSize = iavl.DefaultIAVLCacheSize
	}
	rs := rootmulti.NewStore(db, opts.Logger)
	rs.SetChangesetRecorder(nil) // never record while applying
	rs.SetPruning(opts.Pruning)
	rs.SetIAVLCacheSize(opts.IAVLCacheSize)
	rs.SetIAVLDisableFastNode(opts.DisableFastNode)
	if opts.InterBlockCache {
		rs.SetInterBlockCache(cache.NewCommitKVStoreCacheManager(cache.DefaultCommitKVStoreCacheSize))
	}
	a := &Applier{RS: rs, opts: opts, keys: map[string]types.StoreKey{}}
	for _, d := range manifest {
		a.mount(d.Name, rootmulti.ChangesetToStoreType(d.Type))
	}
	if err := rs.LoadLatestVersion(); err != nil {
		return nil, err
	}
	a.Latest = rs.LastCommitID().Version
	return a, nil
}

func (a *Applier) mount(name string, typ types.StoreType) {
	var key types.StoreKey
	switch typ {
	case types.StoreTypeMemory:
		key = types.NewMemoryStoreKey(name)
	case types.StoreTypeTransient:
		key = types.NewTransientStoreKey(name)
	default:
		key = types.NewKVStoreKey(name)
	}
	a.keys[name] = key
	a.RS.MountStoreWithDB(key, typ, nil)
}

// MismatchError reports an app hash mismatch.
type MismatchError struct {
	Height int64
	Got    []byte
	Want   []byte
	Source string // "header" or "recorded"
	Detail string // per-store differences, if store infos were recorded
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("APP HASH MISMATCH at height %d: computed %X, %s %X%s", e.Height, e.Got, e.Source, e.Want, e.Detail)
}

// Apply applies one block, commits and verifies it.
func (a *Applier) Apply(b *changeset.Block) (types.CommitID, error) {
	if a.Latest != 0 && b.Height != a.Latest+1 {
		return types.CommitID{}, fmt.Errorf("applier: next height must be %d, got %d", a.Latest+1, b.Height)
	}
	if b.InitialVersion != 0 {
		if err := a.RS.SetInitialVersion(b.InitialVersion); err != nil {
			return types.CommitID{}, err
		}
	}
	if b.StoreV1 {
		return types.CommitID{}, fmt.Errorf("applier: height %d was recorded by the SDK v0.50 store (app v4+): use cstool50", b.Height)
	}
	pre, post := b.Stores, []changeset.StoreOps(nil)
	if b.Upgrade != nil {
		// baseapp: flush deferred state while the trees are clean and force
		// the flush of this commit; ops of existing stores (all recorded
		// before the reload or unaffected by it, see CHANGESET.md) go first,
		// the deleted stores' deletes included; then the reload (which runs
		// the native deleteKVStore, now a no-op) and the ops of added stores.
		if err := a.RS.FlushDeferred(); err != nil {
			return types.CommitID{}, err
		}
		a.RS.ForceFlushNextCommit()
		added := map[string]bool{}
		for _, n := range b.Upgrade.Added {
			added[n] = true
		}
		for _, r := range b.Upgrade.Renamed {
			added[r.New] = true
		}
		pre = nil
		for _, s := range b.Stores {
			if added[s.Name] {
				post = append(post, s)
			} else {
				pre = append(pre, s)
			}
		}
	}
	t0 := time.Now()
	if err := a.applyOps(pre); err != nil {
		return types.CommitID{}, err
	}
	if b.Upgrade != nil {
		su := &types.StoreUpgrades{Added: b.Upgrade.Added, Deleted: b.Upgrade.Deleted}
		for _, r := range b.Upgrade.Renamed {
			su.Renamed = append(su.Renamed, types.StoreRename{OldKey: r.Old, NewKey: r.New})
		}
		for _, n := range b.Upgrade.Added {
			if _, ok := a.keys[n]; !ok {
				a.mount(n, types.StoreTypeIAVL)
			}
		}
		for _, r := range b.Upgrade.Renamed {
			if _, ok := a.keys[r.New]; !ok {
				a.mount(r.New, types.StoreTypeIAVL)
			}
		}
		if err := a.RS.LoadLatestVersionAndUpgrade(su); err != nil {
			return types.CommitID{}, fmt.Errorf("height %d: store upgrade: %w", b.Height, err)
		}
	}
	if err := a.applyOps(post); err != nil {
		return types.CommitID{}, err
	}
	t1 := time.Now()
	id := a.RS.Commit()
	a.OpsTime += t1.Sub(t0)
	a.CommitTime += time.Since(t1)
	if b.Upgrade != nil {
		for _, n := range b.Upgrade.Deleted {
			delete(a.keys, n)
		}
		for _, r := range b.Upgrade.Renamed {
			delete(a.keys, r.Old)
		}
	}
	a.Latest = id.Version
	a.Applied++
	a.Ops += int64(b.NumOps())
	if id.Version != b.Height {
		return id, fmt.Errorf("applier: committed version %d for block %d", id.Version, b.Height)
	}
	return id, a.verify(b, id)
}

func (a *Applier) iavlStore(name string) (*iavl.Store, error) {
	key, ok := a.keys[name]
	if !ok {
		return nil, fmt.Errorf("applier: ops for unknown store %q", name)
	}
	st := a.RS.IAVLStore(key)
	if st == nil {
		return nil, fmt.Errorf("applier: store %q is not an IAVL store", name)
	}
	return st, nil
}

func (a *Applier) applyOps(stores []changeset.StoreOps) error {
	if !a.opts.Parallel || len(stores) < 2 {
		for _, s := range stores {
			st, err := a.iavlStore(s.Name)
			if err != nil {
				return err
			}
			if err := st.ApplyOps(s.Ops); err != nil {
				return fmt.Errorf("store %s: %w", s.Name, err)
			}
		}
		return nil
	}
	sts := make([]*iavl.Store, len(stores))
	for i, s := range stores {
		st, err := a.iavlStore(s.Name)
		if err != nil {
			return err
		}
		sts[i] = st
	}
	errs := make([]error, len(stores))
	var wg sync.WaitGroup
	for i := range stores {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = sts[i].ApplyOps(stores[i].Ops)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("store %s: %w", stores[i].Name, err)
		}
	}
	return nil
}

func (a *Applier) verify(b *changeset.Block, id types.CommitID) error {
	if a.Expected != nil {
		want, ok, err := a.Expected(b.Height)
		if err != nil {
			return err
		}
		if ok {
			if !bytes.Equal(want, id.Hash) {
				return &MismatchError{Height: b.Height, Got: id.Hash, Want: want, Source: "trusted header", Detail: a.storeDiff(b)}
			}
			a.CheckedExt++
			// the recorded hash must agree too (else the changeset is mislabelled)
			if len(b.AppHash) > 0 && !bytes.Equal(b.AppHash, id.Hash) {
				return &MismatchError{Height: b.Height, Got: id.Hash, Want: b.AppHash, Source: "recorded (header matched!)"}
			}
			return nil
		}
	}
	if len(b.AppHash) == 0 {
		a.Unverified++
		return nil
	}
	if !bytes.Equal(b.AppHash, id.Hash) {
		return &MismatchError{Height: b.Height, Got: id.Hash, Want: b.AppHash, Source: "recorded", Detail: a.storeDiff(b)}
	}
	a.CheckedLocal++
	return nil
}

func (a *Applier) storeDiff(b *changeset.Block) string {
	if b.StoreInfos == nil {
		return " (no store infos recorded)"
	}
	ci, err := a.RS.GetCommitInfo(b.Height)
	have := map[string][]byte{}
	if err == nil {
		for _, si := range ci.StoreInfos {
			have[si.Name] = si.CommitId.Hash
		}
	} else {
		// deferred persistence: commit info of soft heights is not on disk;
		// use the in-memory last commit info via LastCommitID per store
		for name, key := range a.keys {
			if st := a.RS.GetCommitKVStore(key); st != nil {
				have[name] = st.LastCommitID().Hash
			}
		}
	}
	var diffs []string
	seen := map[string]bool{}
	for _, si := range b.StoreInfos {
		seen[si.Name] = true
		if h, ok := have[si.Name]; !ok {
			diffs = append(diffs, fmt.Sprintf("%s: missing locally", si.Name))
		} else if !bytes.Equal(h, si.Hash) {
			diffs = append(diffs, fmt.Sprintf("%s: computed %X recorded %X", si.Name, h, si.Hash))
		}
	}
	for n := range have {
		if !seen[n] {
			diffs = append(diffs, fmt.Sprintf("%s: not in recorded commit info", n))
		}
	}
	sort.Strings(diffs)
	if len(diffs) == 0 {
		return " (all per-store hashes equal the recorded ones)"
	}
	return "\n  differing stores:\n    " + strings.Join(diffs, "\n    ")
}

// Close flushes deferred state (if any).
func (a *Applier) Close() error {
	if a.RS == nil {
		return nil
	}
	return a.RS.FlushDeferred()
}

// Run opens db, streams changesets from dir and applies them up to (and
// including) to (0 = all). progress is called after every block.
func Run(db dbm.DB, dir string, to int64, opts Options, expected func(int64) ([]byte, bool, error), progress func(*Applier, *changeset.Block)) (*Applier, error) {
	from := rootmulti.GetLatestVersion(db) + 1
	if from == 1 {
		from = 0 // empty DB: start at the first recorded block (genesis or initial height)
	}
	var a *Applier
	errStop := fmt.Errorf("stop")
	err := changeset.Stream(dir, from, func(b *changeset.Block) error {
		if to > 0 && b.Height > to {
			return errStop
		}
		if b.StoreV1 {
			// app v4+ (SDK v0.50 store, IAVL v1): not replayable here; the
			// v0.50 applier (cstool50) continues on this DB from this height
			if a == nil {
				a = &Applier{Latest: b.Height - 1}
			}
			a.HandoverAt = b.Height
			return errStop
		}
		if a == nil {
			var err error
			a, err = Open(db, b.Manifest, opts)
			if err != nil {
				return err
			}
			a.Expected = expected
		}
		if _, err := a.Apply(b); err != nil {
			return err
		}
		if progress != nil {
			progress(a, b)
		}
		return nil
	})
	if err == errStop {
		err = nil
	}
	if a != nil {
		if cerr := a.Close(); err == nil {
			err = cerr
		}
	}
	return a, err
}
