package changeset

import (
	"path/filepath"
	"testing"
)

// A block whose commit finishes after Close (SIGTERM hooks racing the
// in-flight block) must still be recorded, so a restart leaves no gap.
func TestEndBlockAfterCloseIsRecorded(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, Options{BlocksPerFile: 100, Zstd: true})
	if err != nil {
		t.Fatal(err)
	}
	post := []StoreDecl{{Name: "bank", Type: StoreIAVL}}
	for h := int64(1); h <= 3; h++ {
		r.CommitStore("bank", []Op{{Key: []byte{byte(h)}, Value: []byte{1}}})
		r.EndBlock(h, []byte{byte(h)}, nil, post)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r.CommitStore("bank", []Op{{Key: []byte{4}, Value: []byte{1}}})
	r.EndBlock(4, []byte{4}, nil, post) // late block
	var got []int64
	if err := Stream(dir, 1, func(b *Block) error { got = append(got, b.Height); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[3] != 4 {
		t.Fatalf("heights = %v, want 1..4", got)
	}
	// the late block went into its own file, closed (synced) and left
	// uncompressed, next to the compressed file Close finished
	files, err := ListFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || filepath.Ext(files[0]) != ".zst" || filepath.Base(files[1]) != "cs-000000000004.bin" {
		t.Fatalf("files = %v, want the closed .zst file and cs-000000000004.bin", files)
	}
	// a second late block gets a file of its own as well
	r.CommitStore("bank", []Op{{Key: []byte{5}, Value: []byte{1}}})
	r.EndBlock(5, []byte{5}, nil, post)
	if files, err = ListFiles(dir); err != nil || len(files) != 3 {
		t.Fatalf("files = %v (err %v), want 3", files, err)
	}
	got = got[:0]
	if err := Stream(dir, 1, func(b *Block) error { got = append(got, b.Height); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[4] != 5 {
		t.Fatalf("heights = %v, want 1..5", got)
	}
}
