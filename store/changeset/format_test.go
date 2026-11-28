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

func heights(_ *testing.T, dir string) ([]int64, error) {
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
	f, err := os.OpenFile(p, os.O_RDWR, 0) //nolint:gosec // test file
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
	for _, n := range []uint32{MaxFrameSize + 1, 1 << 30, 200 << 20} {
		p, offs := writeTestFile(t, 3)
		f, err := os.OpenFile(p, os.O_RDWR, 0) //nolint:gosec // test file
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

// Unknown flag bits, unknown manifest store types and heights beyond int64
// are decode errors, never silently accepted.
func TestDecodeBlockRejectsUnknown(t *testing.T) {
	good := AppendBlock(nil, testBlock(1))
	_, err := DecodeBlock(good)
	require.NoError(t, err)

	// flags byte follows the uvarint height (1 byte for height 1)
	bad := append([]byte(nil), good...)
	bad[1] |= 1 << 7
	_, err = DecodeBlock(bad)
	require.ErrorContains(t, err, "unknown flag bits")

	b := testBlock(1)
	b.Manifest = []StoreDecl{{Name: "bank", Type: 9}}
	_, err = DecodeBlock(AppendBlock(nil, b))
	require.ErrorContains(t, err, "unknown type 9")
	b.Manifest = []StoreDecl{{Name: "bank", Type: 0}}
	_, err = DecodeBlock(AppendBlock(nil, b))
	require.ErrorContains(t, err, "unknown type 0")

	huge := binary.AppendUvarint(nil, 1<<63)
	_, err = DecodeBlock(append(huge, good[1:]...))
	require.ErrorContains(t, err, "exceeds int64")
}

// Store names must be strictly increasing in the manifest, the store list
// and the store infos, and distinct within each upgrade list.
func TestDecodeBlockRejectsDuplicateOrUnsortedNames(t *testing.T) {
	for name, mut := range map[string]func(b *Block){
		"manifest_dup":     func(b *Block) { b.Manifest = []StoreDecl{{"a", StoreIAVL}, {"a", StoreIAVL}} },
		"manifest_order":   func(b *Block) { b.Manifest = []StoreDecl{{"b", StoreIAVL}, {"a", StoreIAVL}} },
		"stores_dup":       func(b *Block) { b.Stores = append(b.Stores, b.Stores[0]) },
		"stores_order":     func(b *Block) { b.Stores = append([]StoreOps{{Name: "zz"}}, b.Stores...) },
		"storeinfos_dup":   func(b *Block) { b.StoreInfos = []StoreInfo{{Name: "a"}, {Name: "a"}} },
		"storeinfos_order": func(b *Block) { b.StoreInfos = []StoreInfo{{Name: "b"}, {Name: "a"}} },
		"added_dup":        func(b *Block) { b.Upgrade = &Upgrade{Added: []string{"x", "x"}} },
		"deleted_dup":      func(b *Block) { b.Upgrade = &Upgrade{Deleted: []string{"x", "x"}} },
		"renamed_dup":      func(b *Block) { b.Upgrade = &Upgrade{Renamed: []Rename{{"a", "b"}, {"a", "c"}}} },
	} {
		t.Run(name, func(t *testing.T) {
			b := testBlock(1)
			mut(b)
			_, err := DecodeBlock(AppendBlock(nil, b))
			require.Error(t, err)
		})
	}
	// upgrade lists need not be sorted
	b := testBlock(1)
	b.Upgrade = &Upgrade{Added: []string{"z", "a"}}
	_, err := DecodeBlock(AppendBlock(nil, b))
	require.NoError(t, err)
}

// A count field larger than the payload can hold is rejected before any
// allocation sized by it.
func TestDecodeBlockBoundsCounts(t *testing.T) {
	var p []byte
	p = binary.AppendUvarint(p, 1) // height
	p = append(p, 0)               // flags
	p = binary.AppendUvarint(p, 1) // one store
	p = binary.AppendUvarint(p, 1)
	p = append(p, 'a')                   // name
	p = binary.AppendUvarint(p, 1<<40)   // ops
	p = append(p, make([]byte, 1024)...) // a little padding
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := DecodeBlock(p)
	runtime.ReadMemStats(&after)
	require.ErrorContains(t, err, "bad op count")
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(1<<20))

	// and the same for a count the bytes could hold but each element needs 2
	q := binary.AppendUvarint(nil, 1)
	q = append(q, 0)
	q = binary.AppendUvarint(q, 600) // 600 stores need >= 1200 bytes
	q = append(q, make([]byte, 1000)...)
	_, err = DecodeBlock(q)
	require.ErrorContains(t, err, "bad store count")
}

// Writing a payload readers would reject is an error.
func TestWriteFrameRejectsOversizedPayload(t *testing.T) {
	require.Error(t, WriteFrame(io.Discard, make([]byte, MaxFrameSize+1)))
}

// Each store name takes part in at most one change of an upgrade.
func TestDecodeBlockRejectsConflictingUpgrade(t *testing.T) {
	for name, u := range map[string]*Upgrade{
		"rename_to_itself":       {Renamed: []Rename{{"a", "a"}}},
		"rename_chain":           {Renamed: []Rename{{"a", "b"}, {"b", "c"}}},
		"rename_old_added":       {Added: []string{"a"}, Renamed: []Rename{{"a", "b"}}},
		"rename_new_added":       {Added: []string{"b"}, Renamed: []Rename{{"a", "b"}}},
		"rename_old_deleted":     {Deleted: []string{"a"}, Renamed: []Rename{{"a", "b"}}},
		"rename_new_deleted":     {Deleted: []string{"b"}, Renamed: []Rename{{"a", "b"}}},
		"added_and_deleted":      {Added: []string{"a"}, Deleted: []string{"a"}},
		"rename_swap_old_is_new": {Renamed: []Rename{{"a", "b"}, {"c", "a"}}},
	} {
		t.Run(name, func(t *testing.T) {
			b := testBlock(1)
			b.Upgrade = u
			_, err := DecodeBlock(AppendBlock(nil, b))
			require.Error(t, err)
		})
	}
	b := testBlock(1)
	b.Upgrade = &Upgrade{Added: []string{"x"}, Deleted: []string{"y"}, Renamed: []Rename{{"a", "b"}, {"c", "d"}}}
	_, err := DecodeBlock(AppendBlock(nil, b))
	require.NoError(t, err)
}
