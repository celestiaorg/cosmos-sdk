// Package applier replays recorded changesets into a rootmulti store and
// verifies the resulting app hash at every height.
//
// Memory: a block is decoded whole. A frame is at most
// changeset.MaxFrameSize (64 MiB), and a well-formed frame of minimal ops
// decodes to about 28 times its size, so one block can take up to about
// 1.8 GiB in the worst case (real blocks are KB to low MB).
package applier

import (
	"bytes"
	"errors"
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
	// DeferredFlushInterval > 0 applies with deferred IAVL persistence
	// (the catch-up signal is never reported, so it stays on).
	DeferredFlushInterval int64
	// AllowUnverified accepts heights without a trusted app hash, checked
	// against the recorded hash only or not at all (synthetic or test input).
	// By default (false) every height must match a trusted hash from
	// Expected before it stays applied, and Run stops before a height whose
	// trusted hash is not known yet.
	AllowUnverified bool
	Logger          log.Logger
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
	RS    *rootmulti.Store
	db    dbm.DB
	opts  Options
	keys  map[string]types.StoreKey
	types map[string]types.StoreType
	// Expected returns the trusted app hash for a height (e.g. the AppHash
	// of header h+1). ok=false means "unknown": without AllowUnverified the
	// height is not accepted; with it only the recorded hash is checked.
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
	// LastTrusted is the last height verified against a trusted hash.
	LastTrusted int64
	// WaitingFor is the first height Run did not apply because its trusted
	// hash is not known yet (header h+1 missing). Run again once it is.
	WaitingFor int64

	// pending is the height committed but not yet verified; Run rolls it
	// back if the block fails after its commit.
	pending int64
	// verified is the last height that passed verification
	verified int64
	// cur is the block being applied (the deferred flush gate checks it)
	cur *changeset.Block
	// rollback restores the DB to before cur if cur was written but fails
	rollback *rootmulti.ChangesetRollbackPoint

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
	if opts.DeferredFlushInterval > 0 {
		if err := rs.SetDeferredPersistence(rootmulti.DeferredSync, opts.DeferredFlushInterval, 0); err != nil {
			return nil, err
		}
	}
	if opts.InterBlockCache {
		rs.SetInterBlockCache(cache.NewCommitKVStoreCacheManager(cache.DefaultCommitKVStoreCacheSize))
	}
	a := &Applier{RS: rs, db: db, opts: opts, keys: map[string]types.StoreKey{}, types: map[string]types.StoreType{}}
	// a deferred flush never writes a height that has not passed verification
	rs.SetDeferredFlushGate(a.flushGate)
	for _, d := range manifest {
		if !changeset.ValidStoreName(d.Name) {
			return nil, fmt.Errorf("applier: invalid manifest store name %q", d.Name)
		}
		if _, dup := a.keys[d.Name]; dup {
			return nil, fmt.Errorf("applier: manifest lists store %q twice", d.Name)
		}
		typ, ok := rootmulti.ChangesetToStoreType(d.Type)
		if !ok {
			return nil, fmt.Errorf("applier: store %q has unknown type %d", d.Name, d.Type)
		}
		a.mount(d.Name, typ)
	}
	if latest := rootmulti.GetLatestVersion(db); latest > 0 {
		ci, err := rootmulti.ChangesetCommitInfo(db, latest)
		if err == nil && ci == nil {
			err = errors.New("not found")
		}
		if err != nil {
			return nil, fmt.Errorf("applier: commit info of height %d: %w", latest, err)
		}
		if err := a.checkStoreSet(latest, ci, manifest, nil); err != nil {
			return nil, err
		}
	}
	if err := rs.LoadLatestVersion(); err != nil {
		return nil, err
	}
	a.Latest = rs.LastCommitID().Version
	a.verified = a.Latest
	return a, nil
}

// checkStoreSet checks a pre-block manifest and upgrade against the commit
// info of the latest height (as the v0.50 store's applier does): every IAVL
// or DB store the manifest declares exists there, and the upgrade adds or
// renames to absent names and deletes or renames present ones. Stores of the
// commit info the manifest does not declare drop out of the next commit
// (unmounted): logged, and caught by the app hash if wrong.
func (a *Applier) checkStoreSet(latest int64, ci *types.CommitInfo, pre []changeset.StoreDecl, u *changeset.Upgrade) error {
	have := map[string]bool{}
	for _, si := range ci.StoreInfos {
		have[si.Name] = true
	}
	bad := func(format string, args ...any) error {
		return fmt.Errorf("applier: "+format+" (commit info at %d)", append(args, latest)...)
	}
	declared := map[string]bool{}
	for _, d := range pre {
		declared[d.Name] = true
		if (d.Type == changeset.StoreIAVL || d.Type == changeset.StoreDB) && !have[d.Name] {
			return bad("manifest store %q does not exist", d.Name)
		}
	}
	if u != nil {
		for _, n := range append(append(append([]string(nil), u.Added...), u.Deleted...), renameNames(u)...) {
			if !changeset.ValidStoreName(n) {
				return bad("invalid upgrade store name %q", n)
			}
		}
		for _, n := range u.Added {
			if have[n] {
				return bad("upgrade adds existing store %q", n)
			}
		}
		for _, n := range u.Deleted {
			if !have[n] {
				return bad("upgrade deletes missing store %q", n)
			}
		}
		for _, r := range u.Renamed {
			if !have[r.Old] || have[r.New] {
				return bad("upgrade renames %q -> %q, but the old name must exist and the new one must not", r.Old, r.New)
			}
		}
	}
	for _, si := range ci.StoreInfos {
		if !declared[si.Name] {
			a.opts.Logger.Info("changeset applier: store of the commit info is not mounted and drops out", "store", si.Name, "height", latest)
		}
	}
	return nil
}

func renameNames(u *changeset.Upgrade) []string {
	var out []string
	for _, r := range u.Renamed {
		out = append(out, r.Old, r.New)
	}
	return out
}

// manifest returns the current store set.
func (a *Applier) manifest() []changeset.StoreDecl {
	out := make([]changeset.StoreDecl, 0, len(a.types))
	for name, t := range a.types {
		var c byte
		switch t {
		case types.StoreTypeIAVL:
			c = changeset.StoreIAVL
		case types.StoreTypeMemory:
			c = changeset.StoreMemory
		case types.StoreTypeTransient:
			c = changeset.StoreTransient
		case types.StoreTypeDB:
			c = changeset.StoreDB
		}
		out = append(out, changeset.StoreDecl{Name: name, Type: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
	a.types[name] = typ
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
	if a.Latest == 0 {
		// empty DB: the first block is genesis or the initial height
		if first := max(b.InitialVersion, 1); b.Height != first {
			return types.CommitID{}, fmt.Errorf("applier: empty DB: first block must be height %d, got %d", first, b.Height)
		}
	}
	if b.InitialVersion != 0 {
		if err := a.RS.SetInitialVersion(b.InitialVersion); err != nil {
			return types.CommitID{}, err
		}
	}
	if b.StoreV1 {
		return types.CommitID{}, fmt.Errorf("applier: height %d was recorded by the SDK v0.50 store (app v4+): use cstool50", b.Height)
	}
	// every store name the block carries is valid (never '/'), also for a
	// block passed to Apply directly
	names := make([]string, 0, len(b.Manifest)+len(b.Stores)+len(b.StoreInfos))
	for _, d := range b.Manifest {
		names = append(names, d.Name)
	}
	for _, so := range b.Stores {
		names = append(names, so.Name)
	}
	for _, si := range b.StoreInfos {
		names = append(names, si.Name)
	}
	if b.Upgrade != nil {
		names = append(append(names, b.Upgrade.Added...), b.Upgrade.Deleted...)
	}
	for _, n := range names {
		if !changeset.ValidStoreName(n) {
			return types.CommitID{}, fmt.Errorf("applier: height %d: invalid store name %q", b.Height, n)
		}
	}
	if b.Manifest != nil && a.Latest != 0 && !equalDecls(b.Manifest, a.manifest()) {
		return types.CommitID{}, fmt.Errorf("applier: height %d: the block's manifest does not match the current store set", b.Height)
	}
	if b.Upgrade != nil {
		// celestia v1-v3 never renamed a store; a rename would load two trees
		// on one prefix while the old store is still mounted
		if len(b.Upgrade.Renamed) > 0 {
			return types.CommitID{}, fmt.Errorf("applier: height %d: store renames are not supported by the v0.46 applier", b.Height)
		}
		for _, n := range b.Upgrade.Added {
			if !changeset.ValidStoreName(n) {
				return types.CommitID{}, fmt.Errorf("applier: height %d: invalid added store name %q", b.Height, n)
			}
			if _, ok := a.keys[n]; ok {
				return types.CommitID{}, fmt.Errorf("applier: height %d: added store %q is mounted already", b.Height, n)
			}
		}
	}
	if ci := a.RS.LastCommitInfoForChangeset(); a.Latest != 0 && ci != nil {
		if err := a.checkStoreSet(a.Latest, ci, a.manifest(), b.Upgrade); err != nil {
			return types.CommitID{}, fmt.Errorf("height %d: %w", b.Height, err)
		}
	}
	pre, post := b.Stores, []changeset.StoreOps(nil)
	if b.Upgrade != nil {
		// baseapp: flush deferred state while the trees are clean and force
		// the flush of this commit; ops of existing stores (all recorded
		// before the reload or unaffected by it, see the package doc of
		// store/changeset, format.go) go first,
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
			a.mount(n, types.StoreTypeIAVL)
		}
		for _, r := range b.Upgrade.Renamed {
			a.mount(r.New, types.StoreTypeIAVL)
		}
		if err := a.RS.LoadLatestVersionAndUpgrade(su); err != nil {
			return types.CommitID{}, fmt.Errorf("height %d: store upgrade: %w", b.Height, err)
		}
	}
	if err := a.applyOps(post); err != nil {
		return types.CommitID{}, err
	}
	rp, err := a.RS.ChangesetRollbackPoint()
	if err != nil {
		return types.CommitID{}, fmt.Errorf("height %d: %w", b.Height, err)
	}
	a.rollback, a.cur = rp, b
	t1 := time.Now()
	id := a.RS.Commit()
	a.pending = b.Height
	a.OpsTime += t1.Sub(t0)
	a.CommitTime += time.Since(t1)
	if b.Upgrade != nil {
		for _, n := range b.Upgrade.Deleted {
			delete(a.keys, n)
			delete(a.types, n)
		}
		for _, r := range b.Upgrade.Renamed {
			delete(a.keys, r.Old)
			delete(a.types, r.Old)
		}
	}
	a.Latest = id.Version
	a.Applied++
	a.Ops += int64(b.NumOps())
	if id.Version != b.Height {
		return id, fmt.Errorf("applier: committed version %d for block %d", id.Version, b.Height)
	}
	if err := a.verify(b, id); err != nil {
		return id, err
	}
	a.pending = 0
	return id, nil
}

// trusted returns the trusted app hash for height.
func (a *Applier) trusted(height int64) ([]byte, bool, error) {
	if a.Expected == nil {
		return nil, false, nil
	}
	return a.Expected(height)
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

// check verifies hash for b and says against what: "trusted", "recorded"
// or "none" (AllowUnverified without any hash). diff adds per-store
// differences to a mismatch.
func (a *Applier) check(b *changeset.Block, hash []byte, diff bool) (string, error) {
	detail := func() string {
		if diff {
			return a.storeDiff(b)
		}
		return ""
	}
	want, ok, err := a.trusted(b.Height)
	if err != nil {
		return "", fmt.Errorf("applier: height %d: trusted app hash: %w", b.Height, err)
	}
	if ok {
		if !bytes.Equal(want, hash) {
			return "", &MismatchError{Height: b.Height, Got: hash, Want: want, Source: "trusted header", Detail: detail()}
		}
		// the recorded hash must agree too (else the changeset is mislabelled)
		if (len(b.AppHash) > 0 || !a.opts.AllowUnverified) && !bytes.Equal(b.AppHash, hash) {
			return "", &MismatchError{Height: b.Height, Got: hash, Want: b.AppHash, Source: "recorded (header matched!)"}
		}
		return "trusted", nil
	}
	if !a.opts.AllowUnverified {
		return "", fmt.Errorf("applier: no trusted app hash for height %d (set AllowUnverified only for synthetic input)", b.Height)
	}
	if len(b.AppHash) == 0 {
		return "none", nil
	}
	if !bytes.Equal(b.AppHash, hash) {
		return "", &MismatchError{Height: b.Height, Got: hash, Want: b.AppHash, Source: "recorded", Detail: detail()}
	}
	return "recorded", nil
}

func (a *Applier) verify(b *changeset.Block, id types.CommitID) error {
	how, err := a.check(b, id.Hash, true)
	if err != nil {
		return err
	}
	switch how {
	case "trusted":
		a.CheckedExt++
		a.LastTrusted = b.Height
	case "recorded":
		a.CheckedLocal++
	default:
		a.Unverified++
	}
	a.verified = b.Height
	return nil
}

// flushGate approves a deferred flush of version only once it is verified,
// or if it is the block being committed and passes verification.
func (a *Applier) flushGate(version int64, appHash []byte) bool {
	if version <= a.verified {
		return true
	}
	if a.cur == nil || version != a.cur.Height {
		return false
	}
	// "none" (AllowUnverified, no hash at all) is approved too: verify will
	// accept it unchecked, and refusing would keep every pending version in
	// memory and drop the deletes of a removed store
	_, err := a.check(a.cur, appHash, false)
	return err == nil
}

func equalDecls(x, y []changeset.StoreDecl) bool {
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
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
		if !opts.AllowUnverified {
			// never apply a height whose trusted hash is not known yet
			var known bool
			if expected != nil {
				var err error
				if _, known, err = expected(b.Height); err != nil {
					return fmt.Errorf("applier: height %d: trusted app hash: %w", b.Height, err)
				}
			}
			if !known {
				if a == nil {
					a = &Applier{Latest: b.Height - 1}
				}
				a.WaitingFor = b.Height
				return errStop
			}
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
		if err != nil && a.pending != 0 {
			err = a.discardUnverified(db, a.pending, err)
		} else if cerr := a.Close(); err == nil {
			err = cerr
		}
	}
	return a, err
}

// discardUnverified keeps a height that was committed but failed afterwards
// (verification, or an error while verifying) off disk. Close is skipped (it
// would flush the deferred state of that height); a deferred flush never
// wrote it (the flush gate), and a written height is rolled back to the
// exact previous metadata and trees. If that is impossible the error says
// the DB must be discarded.
func (a *Applier) discardUnverified(db dbm.DB, height int64, cause error) error {
	if a.RS == nil {
		return cause
	}
	var note string
	written := rootmulti.GetLatestVersion(db) >= height
	switch {
	case a.rollback == nil || a.rollback.Version() != height-1 && written:
		note = fmt.Sprintf("height %d is on disk and could not be rolled back (no rollback point): discard this DB", height)
	default:
		if err := a.RS.ChangesetRollback(a.rollback); err != nil {
			note = fmt.Sprintf("height %d is on disk and could not be rolled back (%v): discard this DB", height, err)
		} else if written {
			note = fmt.Sprintf("rolled the DB back to height %d", height-1)
		} else {
			note = fmt.Sprintf("height %d did not reach the disk; the DB is at height %d", height, rootmulti.GetLatestVersion(db))
		}
		a.RS = nil // its trees are not usable any more
	}
	var mm *MismatchError
	if errors.As(cause, &mm) {
		mm.Detail += "\n  " + note
		return cause
	}
	return fmt.Errorf("%w; %s", cause, note)
}
