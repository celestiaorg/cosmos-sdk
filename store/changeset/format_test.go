package changeset

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func testBlock(h int64) *Block {
	b := &Block{Height: h, AppHash: []byte{byte(h)}, Stores: []StoreOps{{Name: "bank", Ops: []Op{
		{Kind: OpSet, Key: []byte{1, byte(h)}, Value: []byte("v")},
		{Kind: OpDelete, Key: []byte{2}},
	}}}}
	if h == 1 {
		b.Manifest = []StoreDecl{{Name: "bank", Type: StoreIAVL}}
	}
	return b
}

// writeTestFile writes blocks 1..n and returns the path and frame offsets.
func writeTestFile(t *testing.T, n int64) (string, []int64) {
	p := filepath.Join(t.TempDir(), "cs-000000000001.bin")
	var buf bytes.Buffer
	buf.WriteString(fileMagic)
	offs := []int64{int64(buf.Len())}
	for h := int64(1); h <= n; h++ {
		require.NoError(t, WriteFrame(&buf, AppendBlock(nil, testBlock(h))))
		offs = append(offs, int64(buf.Len()))
	}
	require.NoError(t, os.WriteFile(p, buf.Bytes(), 0o600))
	return p, offs
}

func heights(t *testing.T, dir string) ([]int64, error) {
	var got []int64
	err := Stream(dir, 0, func(b *Block) error { got = append(got, b.Height); return nil })
	return got, err
}

func TestStreamRoundTrip(t *testing.T) {
	p, _ := writeTestFile(t, 5)
	got, err := heights(t, filepath.Dir(p))
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 3, 4, 5}, got)
}

// A frame cut short at the end of the file (crashed writer) is ignored.
func TestStreamTruncatedTail(t *testing.T) {
	p, offs := writeTestFile(t, 5)
	require.NoError(t, os.Truncate(p, offs[4]+5)) // inside frame 5
	got, err := heights(t, filepath.Dir(p))
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 3, 4}, got)
}

func flipByte(t *testing.T, p string, off int64) {
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	require.NoError(t, err)
	defer f.Close()
	b := make([]byte, 1)
	_, err = f.ReadAt(b, off)
	require.NoError(t, err)
	b[0] ^= 0xFF
	_, err = f.WriteAt(b, off)
	require.NoError(t, err)
}

// Damage before the end of the file is an error, never a silent stop.
func TestStreamCorruptMidFile(t *testing.T) {
	p, offs := writeTestFile(t, 5)
	flipByte(t, p, offs[2]+10) // payload of block 3
	_, err := heights(t, filepath.Dir(p))
	require.ErrorIs(t, err, ErrCorrupt)
}

// A checksum mismatch in the very last frame (data not written before a
// power loss) is treated like a truncated tail.
func TestStreamCorruptLastFrame(t *testing.T) {
	p, offs := writeTestFile(t, 5)
	flipByte(t, p, offs[4]+10)
	got, err := heights(t, filepath.Dir(p))
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 3, 4}, got)
}

// A garbage length field must not allocate its size.
func TestFrameLengthBounded(t *testing.T) {
	for _, n := range []uint32{MaxFrameSize + 1, 1 << 30, 40 << 20} {
		p, offs := writeTestFile(t, 3)
		f, err := os.OpenFile(p, os.O_RDWR, 0)
		require.NoError(t, err)
		var hdr [4]byte
		binary.LittleEndian.PutUint32(hdr[:], n)
		_, err = f.WriteAt(hdr[:], offs[1]) // length of block 2
		require.NoError(t, err)
		require.NoError(t, f.Close())

		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		fr, err := OpenFile(p)
		require.NoError(t, err)
		_, err = fr.Next()
		require.NoError(t, err)
		_, err = fr.Next()
		require.Error(t, err)
		if n > MaxFrameSize {
			require.ErrorIs(t, err, ErrCorrupt)
		} else {
			require.ErrorIs(t, err, io.ErrUnexpectedEOF)
		}
		require.NoError(t, fr.Close())
		runtime.ReadMemStats(&after)
		require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(16<<20), "length %d", n)
	}
}

// DecodeBlock must reject arbitrary bytes with an error, never panic.
func FuzzDecodeBlock(f *testing.F) {
	for h := int64(1); h <= 2; h++ {
		f.Add(AppendBlock(nil, testBlock(h)))
	}
	f.Add([]byte{})
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x01})
	f.Fuzz(func(t *testing.T, data []byte) {
		b, err := DecodeBlock(data)
		if err == nil && b == nil {
			t.Fatal("nil block without error")
		}
		if err != nil && errors.Is(err, io.EOF) {
			t.Fatal("io.EOF from a payload decode")
		}
	})
}

func TestWriteFrameRejectsOversize(t *testing.T) {
	require.Error(t, WriteFrame(io.Discard, make([]byte, MaxFrameSize+1)))
	require.NoError(t, WriteFrame(io.Discard, make([]byte, 16)))
}

func decodeErr(t *testing.T, b *Block, contains string) {
	t.Helper()
	_, err := DecodeBlock(AppendBlock(nil, b))
	require.ErrorContains(t, err, contains)
}

// Duplicate store names never decode (they would panic MountStoreWithDB or
// silently merge stores); names must be strictly increasing where the
// recorder writes them sorted, and unique in upgrade lists (app order).
func TestDecodeRejectsDuplicateNames(t *testing.T) {
	ops := []Op{{Kind: OpDelete, Key: []byte{1}}}
	decodeErr(t, &Block{Height: 1, Manifest: []StoreDecl{{"a", StoreIAVL}, {"a", StoreIAVL}}}, "manifest names not strictly increasing")
	decodeErr(t, &Block{Height: 1, Manifest: []StoreDecl{{"b", StoreIAVL}, {"a", StoreIAVL}}}, "manifest names not strictly increasing")
	decodeErr(t, &Block{Height: 1, Stores: []StoreOps{{"a", ops}, {"a", ops}}}, "store names not strictly increasing")
	decodeErr(t, &Block{Height: 1, Stores: []StoreOps{{"b", ops}, {"a", ops}}}, "store names not strictly increasing")
	decodeErr(t, &Block{Height: 1, StoreInfos: []StoreInfo{{Name: "a"}, {Name: "a"}}}, "storeinfo names not strictly increasing")
	decodeErr(t, &Block{Height: 1, Upgrade: &Upgrade{Added: []string{"x", "x"}}}, "duplicate added store")
	decodeErr(t, &Block{Height: 1, Upgrade: &Upgrade{Deleted: []string{"x", "x"}}}, "duplicate deleted store")
	decodeErr(t, &Block{Height: 1, Upgrade: &Upgrade{Renamed: []Rename{{"a", "x"}, {"b", "x"}}}}, "duplicate renamed (new) store")
	decodeErr(t, &Block{Height: 1, Upgrade: &Upgrade{Renamed: []Rename{{"a", "x"}, {"a", "y"}}}}, "duplicate renamed (old) store")

	// upgrade lists keep the app's order: unsorted but unique is fine
	b := &Block{Height: 1, Upgrade: &Upgrade{Added: []string{"z", "a"}, Deleted: []string{"q", "b"}}, AppHash: []byte{1}}
	got, err := DecodeBlock(AppendBlock(nil, b))
	require.NoError(t, err)
	require.Equal(t, b.Upgrade.Added, got.Upgrade.Added)
	require.Equal(t, b.Upgrade.Deleted, got.Upgrade.Deleted)
}

func TestDecodeRejectsUnknownFlagsTypesAndOverflow(t *testing.T) {
	payload := AppendBlock(nil, testBlock(2))
	for _, bit := range []byte{1 << 6, 1 << 7} {
		p := append([]byte{}, payload...)
		p[1] |= bit // height 2 is a one-byte uvarint; flags follow
		_, err := DecodeBlock(p)
		require.ErrorContains(t, err, "unknown flag bits")
	}
	for _, typ := range []byte{0, StoreDB + 1, 0xFF} {
		decodeErr(t, &Block{Height: 1, Manifest: []StoreDecl{{"a", typ}}}, "unknown type")
	}
	over := binary.AppendUvarint(nil, 1<<63)
	_, err := DecodeBlock(append(over, 0))
	require.ErrorContains(t, err, "exceeds int64")
}

// counts claiming far more elements than the payload holds fail at once and
// allocate nothing like their size
func TestDecodeHugeCountsBounded(t *testing.T) {
	huge := binary.AppendUvarint(nil, 1<<40)
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	pad := make([]byte, 64)
	cases := map[string][]byte{
		"manifest":   cat([]byte{1, FlagManifest}, huge, pad),
		"added":      cat([]byte{1, FlagUpgrade}, huge, pad),
		"deleted":    cat([]byte{1, FlagUpgrade, 0}, huge, pad),
		"rename":     cat([]byte{1, FlagUpgrade, 0, 0}, huge, pad),
		"store":      cat([]byte{1, 0}, huge, pad),
		"op":         cat([]byte{1, 0, 1, 1, 'a'}, huge, pad),
		"storeinfo":  cat([]byte{1, FlagStoreInfos, 0, 0}, huge, pad),
		"just_above": cat([]byte{1, 0, 1, 1, 'a', 33}, make([]byte, 65)), // 33 ops need >= 66 bytes
	}
	for name, p := range cases {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := DecodeBlock(p)
		runtime.ReadMemStats(&after)
		require.ErrorContains(t, err, "count", name)
		require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(1<<20), name)
	}
}

// A tiny .zst file expanding into a large frame is rejected with bounded
// memory: the frame is read without allocating its claimed size up front.
func TestZstdFrameBombBounded(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cs-000000000001.bin")
	payload := bytes.Repeat([]byte{0xFF}, 16<<20)
	hdr := []byte{1, 0, 1, 1, 'a'}
	hdr = binary.AppendUvarint(hdr, uint64(len(payload)/2-8)) // passes the count bound
	copy(payload, hdr)
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	require.NoError(t, err)
	_, err = zw.Write([]byte(fileMagic))
	require.NoError(t, err)
	require.NoError(t, WriteFrame(zw, payload))
	require.NoError(t, zw.Close())
	require.NoError(t, os.WriteFile(p+".zst", buf.Bytes(), 0o600))
	payload, buf = nil, bytes.Buffer{}
	st, err := os.Stat(p + ".zst")
	require.NoError(t, err)
	require.Less(t, st.Size(), int64(64<<10))

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = heights(t, dir)
	runtime.ReadMemStats(&after)
	require.ErrorContains(t, err, "changeset: bad uvarint") // 0xFF bytes: the first op fails
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(256<<20))
}
