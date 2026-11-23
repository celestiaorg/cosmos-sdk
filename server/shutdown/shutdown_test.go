package shutdown

import "testing"

func TestRunOnceInOrder(t *testing.T) {
	var got []int
	Register(func() { got = append(got, 1) })
	Register(func() { got = append(got, 2) })
	Run()
	Run()
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("hooks ran %v, want [1 2]", got)
	}
}
