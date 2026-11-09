package trust

import "testing"

func TestCapsAndDecay(t *testing.T) {
	if Score(0, 365, 100, 100) != 0 {
		t.Fatal("inactive history should not grant trust")
	}
	if s := Score(100, 1000, 1000, 1000); s != 100 {
		t.Fatal(s)
	}
	if Score(1, 1, 0, 0) >= Score(10, 30, 5, 10) {
		t.Fatal("activity did not matter")
	}
}
