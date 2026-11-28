// Package changeset records and replays per-block IAVL write sets
// ("changesets") of a rootmulti store (celestia genesis-sync prototype).
//
// A changeset for height H is, per IAVL store, the exact ordered sequence of
// Set/Delete operations that reached the store's MutableTree before it saved
// version H, plus structural events (store upgrades, initial version) and the
// resulting commit info / app hash. Replaying the ops into identical trees in
// the same order reproduces the same IAVL tree shapes and therefore the same
// app hash.
//
// File format (one file per block range, written by Recorder):
//
//	"CSET1\n"
//	frame*   where frame = u32le len | u32le crc32c(payload) | payload
//
// payload (one block):
//
//	uvarint height
//	byte    flags (FlagManifest | FlagUpgrade | FlagInitialVersion | FlagStoreInfos |
//	               FlagUpgradeAtLoad | FlagStoreV1)
//	[manifest]        uvarint n, n x (string name, byte type)          (pre-block store set)
//	[initial version] uvarint
//	[upgrade]         strings added, strings deleted, n x (string old, string new) renamed
//	uvarint nstores, nstores x (string name, uvarint nops, nops x op)
//	  op = byte kind (0 set, 1 delete) | bytes key | [bytes value]
//	bytes app hash
//	[store infos]     uvarint n, n x (string name, uvarint version, bytes hash)
//
// FlagStoreV1 marks blocks recorded by the SDK v0.50 store (cosmossdk.io/store
// forks, IAVL v1; celestia-app v4+). Their app hash excludes memory stores and
// must be replayed with the v0.50 applier; blocks without it come from the
// v0.46 store (app v1-v3). FlagUpgradeAtLoad: the upgrade ran when the process
// loaded the stores before executing this block (v0.50 UpgradeStoreLoader);
// the ops of deleteKVStore/moveKVStoreData are the first ops of the affected
// stores. Without it (v0.46) the upgrade ran inside the Commit of this block,
// after the block's ops. Readers reject unknown flag bits: a block written
// with a newer flag fails to decode instead of being misread.
//
// string/bytes are uvarint length-prefixed. A truncated trailing frame (crash
// while writing) is ignored by the reader. Files may additionally be zstd
// compressed as a whole (suffix .zst).
//
// Files come from outside the node and are decoded as untrusted input: frame
// sizes are bounded (MaxFrameSize), every count is bounded by the bytes left
// in the payload, manifest/store/store-info names must be strictly increasing
// and upgrade lists free of duplicates, and store types must be known.
package changeset

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const (
	fileMagic = "CSET1\n"

	FlagManifest       = 1 << 0
	FlagUpgrade        = 1 << 1
	FlagInitialVersion = 1 << 2
	FlagStoreInfos     = 1 << 3
	FlagUpgradeAtLoad  = 1 << 4
	FlagStoreV1        = 1 << 5
	knownFlags         = FlagManifest | FlagUpgrade | FlagInitialVersion | FlagStoreInfos | FlagUpgradeAtLoad | FlagStoreV1

	OpSet    byte = 0
	OpDelete byte = 1

	// store types in the manifest (mirror of storetypes.StoreType values we need)
	StoreIAVL      byte = 1
	StoreMemory    byte = 2
	StoreTransient byte = 3
	StoreDB        byte = 4
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// Op is one mutation of an IAVL tree.
type Op struct {
	Kind  byte
	Key   []byte
	Value []byte // nil for deletes
}

// StoreOps are the ordered ops one store committed at a height.
type StoreOps struct {
	Name string
	Ops  []Op
}

// StoreDecl declares a mounted store.
type StoreDecl struct {
	Name string
	Type byte
}

// Rename is a renamed store in an upgrade.
type Rename struct{ Old, New string }

// Upgrade mirrors storetypes.StoreUpgrades.
type Upgrade struct {
	Added   []string
	Deleted []string
	Renamed []Rename
}

// StoreInfo is one entry of the commit info.
type StoreInfo struct {
	Name    string
	Version int64
	Hash    []byte
}

// Block is the changeset of one height.
type Block struct {
	Height         int64
	Manifest       []StoreDecl // set on the first block of each file
	InitialVersion int64       // != 0 if SetInitialVersion was called in this block
	Upgrade        *Upgrade
	Stores         []StoreOps // sorted by name
	AppHash        []byte
	StoreInfos     []StoreInfo // optional
	UpgradeAtLoad  bool        // Upgrade ran at store load before the block (v0.50)
	StoreV1        bool        // recorded by the SDK v0.50 store / IAVL v1
}

// NumOps returns the total number of ops in the block.
func (b *Block) NumOps() int {
	n := 0
	for _, s := range b.Stores {
		n += len(s.Ops)
	}
	return n
}

// ---- encoding ----

type enc struct{ b []byte }

func (e *enc) uv(x uint64)    { e.b = binary.AppendUvarint(e.b, x) }
func (e *enc) bytes(p []byte) { e.uv(uint64(len(p))); e.b = append(e.b, p...) }
func (e *enc) str(s string)   { e.uv(uint64(len(s))); e.b = append(e.b, s...) }
func (e *enc) byte1(c byte)   { e.b = append(e.b, c) }
func (e *enc) strs(s []string) {
	e.uv(uint64(len(s)))
	for _, x := range s {
		e.str(x)
	}
}

// AppendBlock appends the payload encoding of b to dst.
func AppendBlock(dst []byte, b *Block) []byte {
	e := enc{b: dst}
	e.uv(uint64(b.Height))
	var flags byte
	if b.Manifest != nil {
		flags |= FlagManifest
	}
	if b.Upgrade != nil {
		flags |= FlagUpgrade
	}
	if b.InitialVersion != 0 {
		flags |= FlagInitialVersion
	}
	if b.StoreInfos != nil {
		flags |= FlagStoreInfos
	}
	if b.UpgradeAtLoad {
		flags |= FlagUpgradeAtLoad
	}
	if b.StoreV1 {
		flags |= FlagStoreV1
	}
	e.byte1(flags)
	if b.Manifest != nil {
		e.uv(uint64(len(b.Manifest)))
		for _, d := range b.Manifest {
			e.str(d.Name)
			e.byte1(d.Type)
		}
	}
	if b.InitialVersion != 0 {
		e.uv(uint64(b.InitialVersion))
	}
	if b.Upgrade != nil {
		e.strs(b.Upgrade.Added)
		e.strs(b.Upgrade.Deleted)
		e.uv(uint64(len(b.Upgrade.Renamed)))
		for _, r := range b.Upgrade.Renamed {
			e.str(r.Old)
			e.str(r.New)
		}
	}
	e.uv(uint64(len(b.Stores)))
	for _, s := range b.Stores {
		e.str(s.Name)
		e.uv(uint64(len(s.Ops)))
		for _, op := range s.Ops {
			e.byte1(op.Kind)
			e.bytes(op.Key)
			if op.Kind == OpSet {
				e.bytes(op.Value)
			}
		}
	}
	e.bytes(b.AppHash)
	if b.StoreInfos != nil {
		e.uv(uint64(len(b.StoreInfos)))
		for _, si := range b.StoreInfos {
			e.str(si.Name)
			e.uv(uint64(si.Version))
			e.bytes(si.Hash)
		}
	}
	return e.b
}

type dec struct {
	b   []byte
	err error
}

func (d *dec) uv() uint64 {
	if d.err != nil {
		return 0
	}
	x, n := binary.Uvarint(d.b)
	if n <= 0 {
		d.err = errors.New("changeset: bad uvarint")
		return 0
	}
	d.b = d.b[n:]
	return x
}

// i64 reads a uvarint that must fit an int64 (heights, versions).
func (d *dec) i64() int64 {
	x := d.uv()
	if x > math.MaxInt64 {
		d.fail("value exceeds int64")
		return 0
	}
	return int64(x)
}

func (d *dec) bytes() []byte {
	n := d.uv()
	if d.err != nil {
		return nil
	}
	if uint64(len(d.b)) < n {
		d.err = errors.New("changeset: short buffer")
		return nil
	}
	p := d.b[:n:n]
	d.b = d.b[n:]
	return p
}

func (d *dec) str() string { return string(d.bytes()) }

func (d *dec) byte1() byte {
	if d.err != nil {
		return 0
	}
	if len(d.b) < 1 {
		d.err = errors.New("changeset: short buffer")
		return 0
	}
	c := d.b[0]
	d.b = d.b[1:]
	return c
}

// count reads an element count and bounds it by the bytes left: every
// element takes at least minSize encoded bytes. Callers allocate with
// capCount, so a count never allocates more than the input can fill.
func (d *dec) count(what string, minSize int) int {
	n := d.uv()
	if d.err != nil {
		return 0
	}
	if n > uint64(len(d.b)/minSize) {
		d.err = fmt.Errorf("changeset: bad %s count %d (%d bytes left)", what, n, len(d.b))
		return 0
	}
	return int(n)
}

// capCount is the initial capacity for n decoded elements; slices grow by
// append as elements actually decode.
func capCount(n int) int { return min(n, 4096) }

func (d *dec) strs(what string) []string {
	n := d.count(what, 1)
	out := make([]string, 0, capCount(n))
	for i := 0; i < n && d.err == nil; i++ {
		out = append(out, d.str())
	}
	return out
}

func (d *dec) fail(format string, args ...any) {
	if d.err == nil {
		d.err = fmt.Errorf("changeset: "+format, args...)
	}
}

// increasing requires names strictly increasing (sorted, no duplicates).
func (d *dec) increasing(what, prev, name string) {
	if name <= prev {
		d.fail("%s names not strictly increasing: %q after %q", what, name, prev)
	}
}

// unique requires names free of duplicates (upgrade lists are in app order).
func (d *dec) unique(what string, names []string) {
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if seen[n] {
			d.fail("duplicate %s store %q", what, n)
			return
		}
		seen[n] = true
	}
}

func validStoreType(t byte) bool { return t >= StoreIAVL && t <= StoreDB }

// DecodeBlock decodes one block payload. Key/value slices alias payload.
func DecodeBlock(payload []byte) (*Block, error) {
	d := &dec{b: payload}
	b := &Block{Height: d.i64()}
	flags := d.byte1()
	if flags&^knownFlags != 0 {
		d.fail("unknown flag bits %#x", flags&^knownFlags)
	}
	b.UpgradeAtLoad = flags&FlagUpgradeAtLoad != 0
	b.StoreV1 = flags&FlagStoreV1 != 0
	if flags&FlagManifest != 0 && d.err == nil {
		n := d.count("manifest", 2)
		b.Manifest = make([]StoreDecl, 0, capCount(n))
		for i := 0; i < n && d.err == nil; i++ {
			sd := StoreDecl{Name: d.str(), Type: d.byte1()}
			if d.err == nil && !validStoreType(sd.Type) {
				d.fail("store %q has unknown type %d", sd.Name, sd.Type)
			}
			if i > 0 {
				d.increasing("manifest", b.Manifest[i-1].Name, sd.Name)
			}
			b.Manifest = append(b.Manifest, sd)
		}
	}
	if flags&FlagInitialVersion != 0 {
		b.InitialVersion = d.i64()
	}
	if flags&FlagUpgrade != 0 && d.err == nil {
		u := &Upgrade{}
		u.Added = d.strs("added")
		u.Deleted = d.strs("deleted")
		n := d.count("rename", 2)
		for i := 0; i < n && d.err == nil; i++ {
			u.Renamed = append(u.Renamed, Rename{Old: d.str(), New: d.str()})
		}
		d.unique("added", u.Added)
		d.unique("deleted", u.Deleted)
		olds, news := make([]string, len(u.Renamed)), make([]string, len(u.Renamed))
		for i, r := range u.Renamed {
			olds[i], news[i] = r.Old, r.New
		}
		d.unique("renamed (old)", olds)
		d.unique("renamed (new)", news)
		b.Upgrade = u
	}
	ns := d.count("store", 2)
	b.Stores = make([]StoreOps, 0, capCount(ns))
	for i := 0; i < ns && d.err == nil; i++ {
		so := StoreOps{Name: d.str()}
		if i > 0 {
			d.increasing("store", b.Stores[i-1].Name, so.Name)
		}
		nops := d.count("op", 2)
		so.Ops = make([]Op, 0, capCount(nops))
		for j := 0; j < nops && d.err == nil; j++ {
			op := Op{Kind: d.byte1(), Key: d.bytes()}
			switch op.Kind {
			case OpSet:
				op.Value = d.bytes()
			case OpDelete:
			default:
				d.fail("bad op kind %d", op.Kind)
			}
			so.Ops = append(so.Ops, op)
		}
		b.Stores = append(b.Stores, so)
	}
	b.AppHash = d.bytes()
	if flags&FlagStoreInfos != 0 && d.err == nil {
		n := d.count("storeinfo", 3)
		b.StoreInfos = make([]StoreInfo, 0, capCount(n))
		for i := 0; i < n && d.err == nil; i++ {
			si := StoreInfo{Name: d.str(), Version: d.i64(), Hash: d.bytes()}
			if i > 0 {
				d.increasing("storeinfo", b.StoreInfos[i-1].Name, si.Name)
			}
			b.StoreInfos = append(b.StoreInfos, si)
		}
	}
	if d.err != nil {
		return nil, d.err
	}
	if len(d.b) != 0 {
		return nil, fmt.Errorf("changeset: %d trailing bytes in block %d", len(d.b), b.Height)
	}
	return b, nil
}

// WriteFrame writes one framed block payload. A payload above MaxFrameSize
// is refused: no reader would accept it.
func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) > MaxFrameSize {
		return fmt.Errorf("changeset: block payload of %d bytes exceeds %d", len(payload), MaxFrameSize)
	}
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint32(hdr[4:8], crc32.Checksum(payload, crcTable))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// ---- reading ----

// FileReader reads blocks from one changeset file.
type FileReader struct {
	path string
	f    *os.File
	zr   *zstd.Decoder
	r    *bufio.Reader
}

// OpenFile opens a .bin or .bin.zst changeset file.
func OpenFile(path string) (*FileReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fr := &FileReader{path: path, f: f}
	var src io.Reader = f
	if strings.HasSuffix(path, ".zst") {
		zr, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(64<<20))
		if err != nil {
			f.Close()
			return nil, err
		}
		fr.zr = zr
		src = zr
	}
	fr.r = bufio.NewReaderSize(src, 1<<20)
	magic := make([]byte, len(fileMagic))
	if _, err := io.ReadFull(fr.r, magic); err != nil || string(magic) != fileMagic {
		fr.Close()
		return nil, fmt.Errorf("changeset: %s: bad magic", path)
	}
	return fr, nil
}

// MaxFrameSize bounds one block payload. Real blocks are far smaller (KB to
// low MB, including the largest store upgrade block); a larger length is
// corruption.
const MaxFrameSize = 64 << 20

// ErrCorrupt reports a changeset file that is damaged before its end.
var ErrCorrupt = errors.New("changeset: corrupt file")

// Next returns the next block, io.EOF at the end. A frame cut short by the
// end of the file (a crashed writer) is reported as io.ErrUnexpectedEOF, as is
// a checksum mismatch in the file's very last frame (unwritten data after a
// power loss). Damage before the end of the file is ErrCorrupt.
func (fr *FileReader) Next() (*Block, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(fr.r, hdr[:]); err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}
		return nil, io.ErrUnexpectedEOF
	}
	n := binary.LittleEndian.Uint32(hdr[0:4])
	crc := binary.LittleEndian.Uint32(hdr[4:8])
	if n > MaxFrameSize {
		if fr.atEOF() {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("%w: %s: frame of %d bytes exceeds %d", ErrCorrupt, fr.path, n, MaxFrameSize)
	}
	// Read through a limited reader so that memory grows with the bytes
	// actually present, not with the length field (which may be garbage).
	// Fresh buffer per block: decoded slices alias it.
	var bb bytes.Buffer
	bb.Grow(int(min(n, 1<<20)))
	if got, err := io.CopyN(&bb, fr.r, int64(n)); err != nil || got != int64(n) {
		return nil, io.ErrUnexpectedEOF
	}
	buf := bb.Bytes()
	if crc32.Checksum(buf, crcTable) != crc {
		if fr.atEOF() {
			return nil, fmt.Errorf("changeset: %s: crc mismatch in the last frame: %w", fr.path, io.ErrUnexpectedEOF)
		}
		return nil, fmt.Errorf("%w: %s: crc mismatch", ErrCorrupt, fr.path)
	}
	return DecodeBlock(buf)
}

// atEOF reports whether nothing follows the current position.
func (fr *FileReader) atEOF() bool {
	_, err := fr.r.Peek(1)
	return err != nil
}

// Close closes the file.
func (fr *FileReader) Close() error {
	if fr.zr != nil {
		fr.zr.Close()
	}
	return fr.f.Close()
}

// ListFiles returns the changeset files in dir ordered by first height (from
// the file name cs-<height>[-<n>].bin[.zst]); for equal first heights the
// later-written file (higher suffix) comes last.
func ListFiles(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type fe struct {
		path string
		h    int64
		seq  int64
	}
	var files []fe
	seen := map[string]bool{}
	for _, e := range ents {
		name := e.Name()
		if !strings.HasPrefix(name, "cs-") {
			continue
		}
		base := strings.TrimSuffix(name, ".zst")
		if !strings.HasSuffix(base, ".bin") {
			continue
		}
		base = strings.TrimSuffix(strings.TrimPrefix(base, "cs-"), ".bin")
		// prefer .zst if both exist (compression finished, .bin not yet removed)
		if seen[base] {
			continue
		}
		var h, seq int64
		if _, err := fmt.Sscanf(base, "%d-%d", &h, &seq); err != nil {
			if _, err := fmt.Sscanf(base, "%d", &h); err != nil {
				continue
			}
		}
		p := filepath.Join(dir, name)
		if !strings.HasSuffix(name, ".zst") {
			if _, err := os.Stat(p + ".zst"); err == nil {
				p += ".zst"
			}
		}
		seen[base] = true
		files = append(files, fe{p, h, seq})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].h != files[j].h {
			return files[i].h < files[j].h
		}
		return files[i].seq < files[j].seq
	})
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.path
	}
	return out, nil
}

// Stream iterates the blocks of all files in dir in height order, starting at
// height from (from <= 0: the first available block). Heights already
// delivered (overlap after a node restart re-recorded blocks) are skipped; a
// gap is an error. The first delivered block always carries a Manifest: the
// pre-block store set, derived from its file's manifest plus the upgrades of
// skipped blocks if the block itself has none (a consumer may start mid-file).
func Stream(dir string, from int64, fn func(*Block) error) error {
	files, err := ListFiles(dir)
	if err != nil {
		return err
	}
	last := from - 1
	delivered := false
	var cur []StoreDecl
	for _, p := range files {
		fr, err := OpenFile(p)
		if err != nil {
			return err
		}
		for {
			b, err := fr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				if errors.Is(err, io.ErrUnexpectedEOF) {
					// truncated tail of a file written by a crashed node
					break
				}
				fr.Close()
				return fmt.Errorf("%s: %w", p, err)
			}
			if b.Manifest != nil {
				cur = append([]StoreDecl(nil), b.Manifest...)
			}
			pre := cur
			if b.Upgrade != nil {
				cur = ApplyUpgrade(cur, b.Upgrade)
			}
			if (from > 0 || delivered) && b.Height <= last {
				continue
			}
			if (delivered || from > 0) && b.Height != last+1 {
				fr.Close()
				return fmt.Errorf("changeset: gap: need height %d, next block in %s is %d", last+1, p, b.Height)
			}
			if !delivered && b.Manifest == nil {
				if pre == nil {
					fr.Close()
					return fmt.Errorf("changeset: no manifest known for height %d", b.Height)
				}
				b.Manifest = append([]StoreDecl(nil), pre...)
			}
			delivered = true
			last = b.Height
			if err := fn(b); err != nil {
				fr.Close()
				return err
			}
		}
		fr.Close()
	}
	if from > 0 && !delivered {
		return fmt.Errorf("changeset: no block at or after height %d in %s", from, dir)
	}
	return nil
}

// ApplyUpgrade returns the store set after applying u to decls.
func ApplyUpgrade(decls []StoreDecl, u *Upgrade) []StoreDecl {
	drop := map[string]bool{}
	for _, d := range u.Deleted {
		drop[d] = true
	}
	for _, r := range u.Renamed {
		drop[r.Old] = true
	}
	var out []StoreDecl
	for _, d := range decls {
		if !drop[d.Name] {
			out = append(out, d)
		}
	}
	have := map[string]bool{}
	for _, d := range out {
		have[d.Name] = true
	}
	for _, a := range u.Added {
		if !have[a] {
			out = append(out, StoreDecl{Name: a, Type: StoreIAVL})
		}
	}
	for _, r := range u.Renamed {
		if !have[r.New] {
			out = append(out, StoreDecl{Name: r.New, Type: StoreIAVL})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
