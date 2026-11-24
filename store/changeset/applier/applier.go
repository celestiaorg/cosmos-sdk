// Package applier replays recorded changesets into a rootmulti store of the
// cosmossdk.io/store module (IAVL v1) and verifies the resulting app hash at
// every height. It rebuilds application state without re-executing
// transactions: it checks app hashes, but produces no events or results and
// does not re-validate state transitions. A height that fails verification
// never stays on disk (see Run).
//
// Differences from the v0.46 applier (github.com/cosmos/cosmos-sdk/store/changeset/applier):
//   - Store upgrades happen at process start (UpgradeStoreLoader), so an
//     upgrade block (FlagUpgradeAtLoad) is applied as: flush, open a fresh
//     rootmulti on the same DB with the new binary's mount set, run
//     LoadLatestVersionAndUpgrade with the native data migration disabled,
//     then apply all recorded ops (the migration's deletes/moves come first
//     in each store), then Commit.
//   - A manifest frame that differs from the mounted set (a new process
//     mounting other stores) reopens the DB with that set.
//   - No inter-block cache is needed (no mid-Commit reload in v0.50).
//   - Deferred persistence ("always" mode) falls back to upstream mode while
//     trees have legacy (IAVL v0.19) versions, i.e. right after the v0.46 ->
//     v0.50 handover; the applier retries enabling it every
//     ReenableDeferredEvery blocks, by when stock pruning has removed them.
package applier

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	dbm "github.com/cosmos/cosmos-db"
	iavltree "github.com/cosmos/iavl"

	"cosmossdk.io/log"
	"cosmossdk.io/store/changeset"
	"cosmossdk.io/store/iavl"
	"cosmossdk.io/store/metrics"
	pruningtypes "cosmossdk.io/store/pruning/types"
	"cosmossdk.io/store/rootmulti"
	"cosmossdk.io/store/types"
)

// Options configure the applier's multistore.
type Options struct {
	Pruning         pruningtypes.PruningOptions // default: everything (as the sync nodes)
	IAVLCacheSize   int                         // default iavl.DefaultIAVLCacheSize
	DisableFastNode bool                        // default true (a node with fast node on rebuilds the index on first start)
	// DeferredFlushInterval > 0 applies with deferred IAVL persistence
	// ("always" mode), flushing every DeferredFlushInterval blocks; 0 = off.
	DeferredFlushInterval int64
	// ReenableDeferredEvery: after a legacy fallback, reopen every N blocks
	// to try enabling deferred persistence again (default 100).
	ReenableDeferredEvery int64
	// Parallel applies the ops of different stores concurrently (independent trees).
	Parallel bool
	// AllowUnverified accepts heights without a trusted hash (Expected unset or
	// unknown for the height), checking only the recorded app hash. Off by
	// default: every height must have a trusted hash before it is applied,
	// and a run stops cleanly at the first height without one.
	AllowUnverified bool
	Logger          log.Logger
	// Logf receives progress notes (reopens, mode changes); nil = stderr.
	Logf func(format string, args ...any)
}

// DefaultOptions returns pruning "everything", fast node off and deferred
// persistence off.
func DefaultOptions() Options {
	return Options{
		Pruning:               pruningtypes.NewPruningOptions(pruningtypes.PruningEverything),
		IAVLCacheSize:         iavl.DefaultIAVLCacheSize,
		DisableFastNode:       true,
		ReenableDeferredEvery: 100,
	}
}

// ErrNoTrustedHash is returned, before anything is applied, for a height
// without a trusted hash (unless Options.AllowUnverified). Run stops cleanly
// on it.
var ErrNoTrustedHash = errors.New("no trusted app hash for this height")

// ErrV046Block is returned for a block recorded by the v0.46 store (app v1-v3).
var ErrV046Block = errors.New("block was recorded by the SDK v0.46 store (app v1-v3): apply it with cstool (v0.46) first")

// Applier owns a rootmulti store opened on a node's application DB.
type Applier struct {
	DB      dbm.DB
	RS      *rootmulti.Store
	opts    Options
	mounted []changeset.StoreDecl // current mount set, sorted by name
	// Expected returns the trusted app hash for a height (e.g. the AppHash of
	// header h+1); ok=false means unknown. Required unless AllowUnverified,
	// which checks only the recorded hash of heights it does not know.
	Expected func(height int64) (hash []byte, ok bool, err error)

	Latest       int64
	LastTrusted  int64 // last height verified against Expected
	Applied      int64
	Ops          int64
	CheckedExt   int64 // heights verified against Expected
	CheckedLocal int64 // heights verified against the recorded hash only
	Unverified   int64 // heights with neither a trusted nor a recorded hash (synthetic input)
	Reopens      int64
	Upgrades     int64

	retryDeferredAt int64
	pending         int64 // committed height not yet verified (0: none)

	OpsTime, CommitTime time.Duration
}

// New returns an applier for db; the stores are opened by the first Apply
// (from its block's manifest) or by Open.
func New(db dbm.DB, opts Options) *Applier {
	if opts.Logger == nil {
		opts.Logger = log.NewNopLogger()
	}
	if opts.IAVLCacheSize == 0 {
		opts.IAVLCacheSize = iavl.DefaultIAVLCacheSize
	}
	if opts.ReenableDeferredEvery <= 0 {
		opts.ReenableDeferredEvery = 100
	}
	if opts.Logf == nil {
		opts.Logf = func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }
	}
	return &Applier{DB: db, opts: opts, Latest: rootmulti.GetLatestVersion(db)}
}

// Open mounts manifest on db and loads the latest version (no upgrade).
func Open(db dbm.DB, manifest []changeset.StoreDecl, opts Options) (*Applier, error) {
	a := New(db, opts)
	if err := a.reopen(manifest, nil); err != nil {
		return nil, err
	}
	return a, nil
}

func sortedDecls(d []changeset.StoreDecl) []changeset.StoreDecl {
	out := append([]changeset.StoreDecl(nil), d...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func sameDecls(a, b []changeset.StoreDecl) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// mountSetForUpgrade returns the store set the upgrading binary mounted:
// the pre-block set minus renamed-old names plus added and renamed-new
// names; deleted stores are still mounted (loadVersion only deletes mounted
// stores).
func mountSetForUpgrade(pre []changeset.StoreDecl, u *changeset.Upgrade) []changeset.StoreDecl {
	drop := map[string]bool{}
	for _, r := range u.Renamed {
		drop[r.Old] = true
	}
	var out []changeset.StoreDecl
	have := map[string]bool{}
	for _, d := range pre {
		if !drop[d.Name] {
			out = append(out, d)
			have[d.Name] = true
		}
	}
	for _, n := range u.Added {
		if !have[n] {
			out = append(out, changeset.StoreDecl{Name: n, Type: changeset.StoreIAVL})
			have[n] = true
		}
	}
	for _, r := range u.Renamed {
		if !have[r.New] {
			out = append(out, changeset.StoreDecl{Name: r.New, Type: changeset.StoreIAVL})
			have[r.New] = true
		}
	}
	return sortedDecls(out)
}

// reopen flushes and releases the current store (if any) and opens a fresh
// rootmulti on the DB with mounts, like a new process: LoadLatestVersion, or
// LoadLatestVersionAndUpgrade(u) (native data migration disabled). With
// deferred persistence requested, trees that still hold legacy-format
// versions are opened in upstream mode and deferred mode is retried later.
func (a *Applier) reopen(mounts []changeset.StoreDecl, u *changeset.Upgrade) error {
	if a.RS != nil {
		if err := a.RS.Close(); err != nil { // flushes deferred state
			return err
		}
		a.RS = nil
		a.Reopens++
	}
	k := a.opts.DeferredFlushInterval
	rs, err := a.open(mounts, u, k)
	if k > 0 && (errors.Is(err, iavltree.ErrDeferredLegacy) || errors.Is(err, iavltree.ErrDeferredHistory)) {
		rs, err = a.open(mounts, u, 0)
		if err == nil {
			a.retryDeferredAt = rs.LastCommitID().Version + a.opts.ReenableDeferredEvery
			a.opts.Logf("applier: deferred persistence off at %d (legacy-format versions); retrying at %d",
				rs.LastCommitID().Version, a.retryDeferredAt)
		}
	} else {
		a.retryDeferredAt = 0
	}
	if err != nil {
		return fmt.Errorf("applier: loading %d stores at %d: %w", len(mounts), a.Latest, err)
	}
	a.RS = rs
	a.mounted = sortedDecls(mounts)
	a.Latest = rs.LastCommitID().Version
	return nil
}

func (a *Applier) open(mounts []changeset.StoreDecl, u *changeset.Upgrade, flushInterval int64) (*rootmulti.Store, error) {
	rs := rootmulti.NewStore(a.DB, a.opts.Logger, metrics.NewNoOpMetrics())
	rs.SetChangesetRecorder(nil) // never record while applying
	rs.SetChangesetApplyMode(true)
	rs.SetPruning(a.opts.Pruning)
	rs.SetIAVLCacheSize(a.opts.IAVLCacheSize)
	rs.SetIAVLDisableFastNode(a.opts.DisableFastNode)
	seen := map[string]bool{}
	for _, d := range mounts {
		if seen[d.Name] {
			return nil, fmt.Errorf("applier: store %q declared twice", d.Name)
		}
		seen[d.Name] = true
	}
	if flushInterval > 0 {
		if err := rs.SetDeferredPersistence(rootmulti.DeferredAlways, flushInterval); err != nil {
			return nil, err
		}
	}
	for _, d := range mounts {
		typ, err := rootmulti.ChangesetToStoreType(d.Type)
		if err != nil {
			return nil, fmt.Errorf("applier: store %q: %w", d.Name, err)
		}
		var key types.StoreKey
		switch typ {
		case types.StoreTypeMemory:
			key = types.NewMemoryStoreKey(d.Name)
		case types.StoreTypeTransient:
			key = types.NewTransientStoreKey(d.Name)
		default:
			key = types.NewKVStoreKey(d.Name)
		}
		rs.MountStoreWithDB(key, typ, nil)
	}
	var err error
	if u != nil {
		su := &types.StoreUpgrades{Added: u.Added, Deleted: u.Deleted}
		for _, r := range u.Renamed {
			su.Renamed = append(su.Renamed, types.StoreRename{OldKey: r.Old, NewKey: r.New})
		}
		err = rs.LoadLatestVersionAndUpgrade(su)
	} else {
		err = rs.LoadLatestVersion()
	}
	if err != nil {
		return nil, err
	}
	return rs, nil
}

// MismatchError reports an app hash mismatch.
type MismatchError struct {
	Height int64
	Got    []byte
	Want   []byte
	Source string // "trusted header" or "recorded"
	Detail string // per-store differences, if store infos were recorded
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("APP HASH MISMATCH at height %d: computed %X, %s %X%s", e.Height, e.Got, e.Source, e.Want, e.Detail)
}

// Apply applies one block, commits and verifies it. Unless AllowUnverified,
// the trusted hash of the height is required before anything is applied. An
// error after the commit leaves the height pending (see Run).
func (a *Applier) Apply(b *changeset.Block) (types.CommitID, error) {
	if a.pending != 0 {
		return types.CommitID{}, fmt.Errorf("applier: height %d failed verification; discard it first", a.pending)
	}
	if !b.StoreV1 {
		return types.CommitID{}, fmt.Errorf("height %d: %w", b.Height, ErrV046Block)
	}
	if a.Latest != 0 && b.Height != a.Latest+1 {
		return types.CommitID{}, fmt.Errorf("applier: next height must be %d, got %d", a.Latest+1, b.Height)
	}
	if a.Latest == 0 && b.Height != max(b.InitialVersion, 1) {
		return types.CommitID{}, fmt.Errorf("applier: empty DB: first block must be height %d, got %d", max(b.InitialVersion, 1), b.Height)
	}
	trusted, err := a.trustedHash(b)
	if err != nil {
		return types.CommitID{}, err
	}
	if b.Upgrade != nil && !b.UpgradeAtLoad {
		return types.CommitID{}, fmt.Errorf("applier: height %d: store-v1 upgrade without FlagUpgradeAtLoad", b.Height)
	}
	pre := a.mounted
	if b.Manifest != nil {
		pre = sortedDecls(b.Manifest)
	}
	switch {
	case b.Upgrade != nil:
		if err := a.reopen(mountSetForUpgrade(pre, b.Upgrade), b.Upgrade); err != nil {
			return types.CommitID{}, fmt.Errorf("height %d: store upgrade: %w", b.Height, err)
		}
		a.mounted = sortedDecls(changeset.ApplyUpgrade(pre, b.Upgrade)) // after the commit
		a.Upgrades++
	case a.RS == nil || !sameDecls(pre, a.mounted):
		if a.RS != nil {
			a.opts.Logf("applier: height %d: mounted store set changed (new process), reopening", b.Height)
		}
		if err := a.reopen(pre, nil); err != nil {
			return types.CommitID{}, err
		}
	case a.retryDeferredAt > 0 && b.Height >= a.retryDeferredAt:
		if err := a.reopen(a.mounted, nil); err != nil {
			return types.CommitID{}, err
		}
		if a.retryDeferredAt == 0 {
			a.opts.Logf("applier: deferred persistence enabled at %d", a.Latest)
		}
	}
	if a.Latest != 0 && b.Height != a.Latest+1 {
		return types.CommitID{}, fmt.Errorf("applier: DB is at %d, block is %d", a.Latest, b.Height)
	}
	if b.InitialVersion != 0 {
		if err := a.RS.SetInitialVersion(b.InitialVersion); err != nil {
			return types.CommitID{}, err
		}
	}
	t0 := time.Now()
	if err := a.applyOps(b.Stores); err != nil {
		return types.CommitID{}, fmt.Errorf("height %d: %w", b.Height, err)
	}
	t1 := time.Now()
	id := a.RS.Commit()
	a.pending = id.Version
	a.OpsTime += t1.Sub(t0)
	a.CommitTime += time.Since(t1)
	a.Latest = id.Version
	a.Applied++
	a.Ops += int64(b.NumOps())
	if id.Version != b.Height {
		return id, fmt.Errorf("applier: committed version %d for block %d", id.Version, b.Height)
	}
	if err := a.verify(b, id, trusted); err != nil {
		return id, err
	}
	a.pending = 0
	return id, nil
}

// trustedHash returns the trusted hash of b's height (nil if unknown and
// AllowUnverified).
func (a *Applier) trustedHash(b *changeset.Block) ([]byte, error) {
	var (
		want []byte
		ok   bool
	)
	if a.Expected != nil {
		var err error
		if want, ok, err = a.Expected(b.Height); err != nil {
			return nil, fmt.Errorf("height %d: trusted hash: %w", b.Height, err)
		}
	}
	if a.opts.AllowUnverified {
		return want, nil
	}
	if !ok || len(want) == 0 {
		return nil, fmt.Errorf("height %d: %w", b.Height, ErrNoTrustedHash)
	}
	if len(b.AppHash) == 0 {
		return nil, fmt.Errorf("applier: height %d: the changeset has no recorded app hash", b.Height)
	}
	return want, nil
}

func (a *Applier) iavlStore(name string) (*iavl.Store, error) {
	st := a.RS.IAVLStoreByName(name)
	if st == nil {
		return nil, fmt.Errorf("applier: ops for store %q, which is not a loaded IAVL store", name)
	}
	return st, nil
}

func (a *Applier) applyOps(stores []changeset.StoreOps) error {
	sts := make([]*iavl.Store, len(stores))
	for i, s := range stores {
		st, err := a.iavlStore(s.Name)
		if err != nil {
			return err
		}
		sts[i] = st
	}
	if !a.opts.Parallel || len(stores) < 2 {
		for i, s := range stores {
			if err := sts[i].ApplyOps(s.Ops); err != nil {
				return fmt.Errorf("store %s: %w", s.Name, err)
			}
		}
		return nil
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

func (a *Applier) verify(b *changeset.Block, id types.CommitID, want []byte) error {
	if want != nil {
		if !bytes.Equal(want, id.Hash) {
			return &MismatchError{Height: b.Height, Got: id.Hash, Want: want, Source: "trusted header", Detail: a.storeDiff(b)}
		}
		// the recorded hash must agree too (else the changeset is mislabelled)
		if len(b.AppHash) > 0 && !bytes.Equal(b.AppHash, id.Hash) {
			return &MismatchError{Height: b.Height, Got: id.Hash, Want: b.AppHash, Source: "recorded (header matched!)"}
		}
		a.CheckedExt++
		a.LastTrusted = b.Height
		return nil
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
	have := map[string][]byte{}
	if ci := a.RS.LastCommitInfoForChangeset(); ci != nil {
		for _, si := range ci.StoreInfos {
			have[si.Name] = si.CommitId.Hash
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

// Close flushes deferred state (if any) and releases the trees; the DB stays
// open. Do not call it after a MismatchError (see Run).
func (a *Applier) Close() error {
	if a.RS == nil {
		return nil
	}
	err := a.RS.Close()
	a.RS = nil
	return err
}

// Run streams changesets from dir into db, starting after the DB's latest
// version, and applies them up to (and including) to (0 = all). progress is
// called after every block. Unless opts.AllowUnverified, it stops cleanly
// (nil error) before the first height without a trusted hash; LastTrusted is
// the last applied height. On an error after a commit the failing height is
// kept off disk: the deferred flush of Close is skipped, and a height an
// upstream commit (or a deferred flush) already wrote is rolled back; if
// that is impossible, the error says to discard the DB.
func Run(db dbm.DB, dir string, to int64, opts Options, expected func(int64) ([]byte, bool, error), progress func(*Applier, *changeset.Block)) (*Applier, error) {
	a := New(db, opts)
	a.Expected = expected
	from := a.Latest + 1
	if from == 1 {
		from = 0 // empty DB: start at the first recorded block (genesis or initial height)
	}
	errStop := errors.New("stop")
	err := changeset.Stream(dir, from, func(b *changeset.Block) error {
		if to > 0 && b.Height > to {
			return errStop
		}
		if _, err := a.Apply(b); err != nil {
			return err
		}
		if progress != nil {
			progress(a, b)
		}
		return nil
	})
	if errors.Is(err, errStop) {
		err = nil
	}
	if errors.Is(err, ErrNoTrustedHash) && a.pending == 0 {
		a.opts.Logf("applier: %v; stopping at %d", err, a.Latest)
		err = nil
	}
	if a.pending != 0 {
		return a, a.discardPending(err)
	}
	if cerr := a.Close(); err == nil {
		err = cerr
	}
	return a, err
}

// discardPending keeps the committed but unverified height off disk and
// returns err with what happened appended.
func (a *Applier) discardPending(err error) error {
	h := a.pending
	note := fmt.Sprintf("height %d was not written", h)
	if rootmulti.GetLatestVersion(a.DB) >= h {
		if rerr := a.RS.RollbackToVersion(h - 1); rerr != nil {
			note = fmt.Sprintf("height %d is on disk and could not be rolled back (%v): discard this DB", h, rerr)
		} else {
			note = fmt.Sprintf("rolled the DB back to height %d", h-1)
		}
	}
	var mm *MismatchError
	if errors.As(err, &mm) {
		mm.Detail += "\n  " + note
		return err
	}
	return fmt.Errorf("%w (%s)", err, note)
}
