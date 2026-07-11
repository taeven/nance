package service

import "testing"

func TestEscapePromLabelValue(t *testing.T) {
	if got := escapePromLabelValue(`a"b\c`); got != `a\"b\\c` {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeWindow(t *testing.T) {
	w, err := NormalizeWindow("", "1h")
	if err != nil || w != "1h" {
		t.Fatalf("%v %v", w, err)
	}
	if _, err := NormalizeWindow("2h", "1h"); err == nil {
		t.Fatal("expected error")
	}
}
