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
	for _, n := range []uint32{MaxFrameSize + 1, 1 << 30, 200 << 20} {
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
