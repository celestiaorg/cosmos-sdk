package changeset

import "testing"

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
}
