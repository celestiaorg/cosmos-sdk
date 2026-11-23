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
// after the block's ops. Older readers ignore both bits.
//
// string/bytes are uvarint length-prefixed. A truncated trailing frame (crash
// while writing) is ignored by the reader. Files may additionally be zstd
// compressed as a whole (suffix .zst).
package changeset

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
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

func (d *dec) strs() []string {
	n := d.uv()
	if d.err != nil || n > uint64(len(d.b)) {
		if d.err == nil {
			d.err = errors.New("changeset: bad count")
		}
		return nil
	}
	out := make([]string, n)
	for i := range out {
		out[i] = d.str()
	}
	return out
}

// DecodeBlock decodes one block payload. Key/value slices alias payload.
func DecodeBlock(payload []byte) (*Block, error) {
	d := &dec{b: payload}
	b := &Block{Height: int64(d.uv())}
	flags := d.byte1()
	b.UpgradeAtLoad = flags&FlagUpgradeAtLoad != 0
	b.StoreV1 = flags&FlagStoreV1 != 0
	if flags&FlagManifest != 0 {
		n := d.uv()
		if n > uint64(len(d.b)) {
			return nil, errors.New("changeset: bad manifest count")
		}
		b.Manifest = make([]StoreDecl, n)
		for i := range b.Manifest {
			b.Manifest[i].Name = d.str()
			b.Manifest[i].Type = d.byte1()
		}
	}
	if flags&FlagInitialVersion != 0 {
		b.InitialVersion = int64(d.uv())
	}
	if flags&FlagUpgrade != 0 {
		u := &Upgrade{}
		u.Added = d.strs()
		u.Deleted = d.strs()
		n := d.uv()
		if n > uint64(len(d.b)) {
			return nil, errors.New("changeset: bad rename count")
		}
		for i := uint64(0); i < n; i++ {
			u.Renamed = append(u.Renamed, Rename{Old: d.str(), New: d.str()})
		}
		b.Upgrade = u
	}
	ns := d.uv()
	if ns > uint64(len(d.b)) {
		return nil, errors.New("changeset: bad store count")
	}
	b.Stores = make([]StoreOps, ns)
	for i := range b.Stores {
		b.Stores[i].Name = d.str()
		nops := d.uv()
		if nops > uint64(len(d.b)) {
			return nil, errors.New("changeset: bad op count")
		}
		ops := make([]Op, nops)
		for j := range ops {
			ops[j].Kind = d.byte1()
			ops[j].Key = d.bytes()
			switch ops[j].Kind {
			case OpSet:
				ops[j].Value = d.bytes()
			case OpDelete:
			default:
				if d.err == nil {
					d.err = fmt.Errorf("changeset: bad op kind %d", ops[j].Kind)
				}
			}
		}
		b.Stores[i].Ops = ops
	}
	b.AppHash = d.bytes()
	if flags&FlagStoreInfos != 0 {
		n := d.uv()
		if n > uint64(len(d.b)) {
			return nil, errors.New("changeset: bad storeinfo count")
		}
		b.StoreInfos = make([]StoreInfo, n)
		for i := range b.StoreInfos {
			b.StoreInfos[i].Name = d.str()
			b.StoreInfos[i].Version = int64(d.uv())
			b.StoreInfos[i].Hash = d.bytes()
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

// WriteFrame writes one framed block payload.
func WriteFrame(w io.Writer, payload []byte) error {
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
	buf  []byte
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

// MaxFrameSize bounds one block payload. Real blocks are far smaller (tens
// of KB); a larger length is corruption.
const MaxFrameSize = 256 << 20

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
			if from > 0 && b.Height <= last {
				continue
			}
			if delivered && b.Height <= last {
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
