package changeset

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"

	"github.com/klauspost/compress/zstd" //nolint:depguard // zstd-compressed changeset files
)

// Environment variables of the env-gated recorder.
const (
	EnvOut           = "CELESTIA_CHANGESET_OUT"             // output directory; recording is on iff set
	EnvBlocksPerFile = "CELESTIA_CHANGESET_BLOCKS_PER_FILE" // default 10000
	EnvStoreInfos    = "CELESTIA_CHANGESET_STOREINFOS"      // "0" disables per-store hashes (default on)
	EnvZstd          = "CELESTIA_CHANGESET_ZSTD"            // "0" keeps finished files uncompressed (default: zstd)
)

// Options configure a Recorder.
type Options struct {
	BlocksPerFile int64
	StoreInfos    bool
	Zstd          bool
}

// Recorder collects the per-store ops committed at each height and writes
// one framed block per Commit. Safe for concurrent use (the multistore may
// commit stores concurrently, see rootmulti.SetParallelCommit; blocks list
// stores sorted by name).
//
// cosmossdk.io/store variant: every block carries FlagStoreV1; store upgrades
// are recorded at the end of a successful rootmulti load (UpgradeAtLoad).
//
// A write error panics: the log must never silently skip a block or hold a
// partial one that looks valid.
type Recorder struct {
	mu   sync.Mutex
	dir  string
	opts Options

	// pending (current block)
	stores         map[string][]Op
	upgrade        *Upgrade
	upgradeAtLoad  bool
	initialVersion int64

	// output
	f         *os.File
	w         *bufio.Writer
	path      string
	nInFile   int64
	lastH     int64
	buf       []byte
	compWG    sync.WaitGroup
	closed    bool
	TotalOps  int64
	TotalByte int64
	Blocks    int64
}

var (
	envOnce sync.Once
	envRec  *Recorder
)

// FromEnv returns the process-wide recorder configured by CELESTIA_CHANGESET_OUT
// (nil if unset, which leaves the store exactly as without this package).
// Panics if the directory cannot be created or a setting is invalid.
func FromEnv() *Recorder {
	envOnce.Do(func() {
		dir := os.Getenv(EnvOut)
		if dir == "" {
			return
		}
		opts := Options{BlocksPerFile: 10000, StoreInfos: true, Zstd: true}
		if s := os.Getenv(EnvBlocksPerFile); s != "" {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil || n <= 0 {
				panic(fmt.Sprintf("invalid %s=%q", EnvBlocksPerFile, s))
			}
			opts.BlocksPerFile = n
		}
		if os.Getenv(EnvStoreInfos) == "0" {
			opts.StoreInfos = false
		}
		if os.Getenv(EnvZstd) == "0" {
			opts.Zstd = false
		}
		r, err := New(dir, opts)
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(os.Stderr, "changeset recorder enabled (store v1): %s=%s blocks/file=%d storeinfos=%v zstd=%v\n",
			EnvOut, dir, opts.BlocksPerFile, opts.StoreInfos, opts.Zstd)
		// No shutdown hook (the store module cannot import the SDK): every
		// block is written with one write syscall, the file is fsynced before
		// the commit info that makes a height durable, and rootmulti.Close
		// (called by App.Close) closes the recorder. A restart opens a new file.
		envRec = r
	})
	return envRec
}

// New creates a recorder writing into dir.
func New(dir string, opts Options) (*Recorder, error) {
	if opts.BlocksPerFile <= 0 {
		opts.BlocksPerFile = 10000
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Recorder{dir: dir, opts: opts, stores: map[string][]Op{}}, nil
}

// CommitStore records the ops a store committed in the current block. Called
// from iavl.Store.Commit after a successful SaveVersion.
func (r *Recorder) CommitStore(name string, ops []Op) {
	if len(ops) == 0 {
		return
	}
	r.mu.Lock()
	r.stores[name] = append(r.stores[name], ops...)
	r.mu.Unlock()
}

// RecordUpgradeAtLoad records the store upgrade of a successful
// rootmulti.loadVersion (v0.50: UpgradeStoreLoader at process start). It
// replaces a pending one, so a load that is retried does not duplicate names.
// Ops of a failed attempt were buffered on discarded store instances and are
// never recorded.
func (r *Recorder) RecordUpgradeAtLoad(u *Upgrade) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.upgrade = u
	r.upgradeAtLoad = true
}

// RecordInitialVersion records rootmulti.SetInitialVersion (InitChain).
func (r *Recorder) RecordInitialVersion(v int64) {
	r.mu.Lock()
	r.initialVersion = v
	r.mu.Unlock()
}

// EndBlock writes the block for height. post is the store set after the
// commit (used to derive the pre-block manifest at the start of a file).
func (r *Recorder) EndBlock(height int64, appHash []byte, infos []StoreInfo, post []StoreDecl) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// A block can finish committing after Close ran: the SIGTERM hooks may
	// close the recorder while the in-flight block is still in Commit, and the
	// deferred-persistence shutdown flush then persists that block. Dropping it
	// would leave a one-block gap at every restart, so write it into its own
	// file (synced, left uncompressed; readers accept .bin) instead.
	late := r.closed
	b := &Block{
		Height: height, AppHash: appHash, InitialVersion: r.initialVersion, Upgrade: r.upgrade,
		UpgradeAtLoad: r.upgrade != nil && r.upgradeAtLoad, StoreV1: true,
	}
	names := make([]string, 0, len(r.stores))
	for n := range r.stores {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b.Stores = append(b.Stores, StoreOps{Name: n, Ops: r.stores[n]})
	}
	if r.opts.StoreInfos {
		b.StoreInfos = infos
	}
	r.stores = map[string][]Op{}
	r.upgrade = nil
	r.upgradeAtLoad = false
	r.initialVersion = 0

	if err := r.writeLocked(b, post); err != nil {
		// recording must never silently produce a gapped/corrupt log
		panic(fmt.Sprintf("changeset recorder: height %d: %v", height, err))
	}
	if late {
		if err := r.closeFileSyncLocked(); err != nil {
			panic(fmt.Sprintf("changeset recorder: height %d (after close): %v", height, err))
		}
	}
}

func (r *Recorder) writeLocked(b *Block, post []StoreDecl) error {
	if r.f != nil && (r.nInFile >= r.opts.BlocksPerFile || b.Height != r.lastH+1) {
		if err := r.rotateLocked(); err != nil {
			return err
		}
	}
	if r.f == nil {
		b.Manifest = preManifest(post, b.Upgrade)
		if err := r.openLocked(b.Height); err != nil {
			return err
		}
	}
	r.buf = AppendBlock(r.buf[:0], b)
	if err := WriteFrame(r.w, r.buf); err != nil {
		return err
	}
	// one write syscall per block (no fsync): a crash loses at most the
	// partially written frame, which readers ignore
	if err := r.w.Flush(); err != nil {
		return err
	}
	r.nInFile++
	r.lastH = b.Height
	r.Blocks++
	r.TotalOps += int64(b.NumOps())
	r.TotalByte += int64(len(r.buf) + 8)
	return nil
}

// preManifest reverses an upgrade on the post-commit store set.
func preManifest(post []StoreDecl, u *Upgrade) []StoreDecl {
	m := map[string]byte{}
	for _, d := range post {
		m[d.Name] = d.Type
	}
	if u != nil {
		for _, a := range u.Added {
			delete(m, a)
		}
		for _, rn := range u.Renamed {
			delete(m, rn.New)
			m[rn.Old] = StoreIAVL
		}
		for _, d := range u.Deleted {
			m[d] = StoreIAVL
		}
	}
	out := make([]StoreDecl, 0, len(m))
	for n, t := range m {
		out = append(out, StoreDecl{Name: n, Type: t})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Recorder) openLocked(first int64) error {
	p := filepath.Join(r.dir, fmt.Sprintf("cs-%012d.bin", first))
	for seq := 1; ; seq++ {
		_, e1 := os.Stat(p)
		_, e2 := os.Stat(p + ".zst")
		if os.IsNotExist(e1) && os.IsNotExist(e2) {
			break
		}
		p = filepath.Join(r.dir, fmt.Sprintf("cs-%012d-%d.bin", first, seq))
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // path under the recorder directory
	if err != nil {
		return err
	}
	r.f, r.path, r.nInFile = f, p, 0
	r.w = bufio.NewWriterSize(f, 1<<20)
	_, err = r.w.WriteString(fileMagic)
	return err
}

func (r *Recorder) rotateLocked() error {
	if r.f == nil {
		return nil
	}
	if err := r.w.Flush(); err != nil {
		return err
	}
	if err := r.f.Sync(); err != nil {
		return err
	}
	if err := r.f.Close(); err != nil {
		return err
	}
	p := r.path
	r.f, r.w, r.path = nil, nil, ""
	if r.opts.Zstd {
		r.compWG.Add(1)
		go func() {
			defer r.compWG.Done()
			if err := CompressFile(p); err != nil {
				fmt.Fprintf(os.Stderr, "changeset recorder: compressing %s: %v (left uncompressed)\n", p, err)
			}
		}()
	}
	return nil
}

// Sync writes buffered data of the open file to disk (fsync); the file stays open.
func (r *Recorder) Sync() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	if err := r.w.Flush(); err != nil {
		return err
	}
	return r.f.Sync()
}

// Close finishes the current file and waits for background compression.
func (r *Recorder) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	err := r.rotateLocked()
	r.mu.Unlock()
	r.compWG.Wait()
	return err
}

// CompressFile zstd-compresses p into p+".zst" and removes p.
func CompressFile(p string) error {
	in, err := os.Open(p) //nolint:gosec // path under the recorder directory
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := p + ".zst.tmp"
	out, err := os.Create(tmp) //nolint:gosec // path under the recorder directory
	if err != nil {
		return err
	}
	zw, err := zstd.NewWriter(out, zstd.WithEncoderLevel(zstd.SpeedBetterCompression), zstd.WithEncoderConcurrency(1))
	if err != nil {
		_ = out.Close()
		return err
	}
	if _, err := io.Copy(zw, in); err != nil {
		_ = zw.Close()
		_ = out.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, p+".zst"); err != nil {
		return err
	}
	return os.Remove(p)
}

// ---- writer for tools (no pending state) ----

// FileWriter writes blocks to a single file (used by tools/tests).
type FileWriter struct {
	f   *os.File
	w   *bufio.Writer
	buf []byte
	N   int64
}

// CreateFile creates a changeset file at p.
func CreateFile(p string) (*FileWriter, error) {
	f, err := os.Create(p) //nolint:gosec // caller-supplied output path (tools, tests)
	if err != nil {
		return nil, err
	}
	fw := &FileWriter{f: f, w: bufio.NewWriterSize(f, 1<<20)}
	_, err = fw.w.WriteString(fileMagic)
	return fw, err
}

// Write appends a block.
func (fw *FileWriter) Write(b *Block) error {
	fw.buf = AppendBlock(fw.buf[:0], b)
	fw.N += int64(len(fw.buf) + 8)
	return WriteFrame(fw.w, fw.buf)
}

// Close flushes and closes.
func (fw *FileWriter) Close() error {
	if err := fw.w.Flush(); err != nil {
		_ = fw.f.Close()
		return err
	}
	return fw.f.Close()
}

// closeFileSyncLocked flushes, syncs and closes the current file without
// background compression (used for blocks that arrive after Close).
func (r *Recorder) closeFileSyncLocked() error {
	if r.f == nil {
		return nil
	}
	if err := r.w.Flush(); err != nil {
		return err
	}
	if err := r.f.Sync(); err != nil {
		return err
	}
	err := r.f.Close()
	r.f, r.w, r.path = nil, nil, ""
	return err
}
