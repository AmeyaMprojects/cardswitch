package routing

import "testing"

func TestLookupLongestPrefix(t *testing.T) {
	tbl := DefaultTable()
	r, ok := tbl.Lookup("41111122")
	if !ok || r.Prefix != "411111" {
		t.Fatalf("want 411111, got %+v ok=%v", r, ok)
	}
	if _, ok := tbl.Lookup("9999"); ok {
		t.Fatal("expected no route")
	}
}
