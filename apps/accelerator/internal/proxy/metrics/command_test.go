package metrics

import "testing"

func TestCommandLabel(t *testing.T) {
	if got := CommandLabel("find"); got != "find" {
		t.Fatalf("got %q", got)
	}
	if got := CommandLabel("Find"); got != "find" {
		t.Fatalf("got %q", got)
	}
	if got := CommandLabel("evil_unknown_cmd_xyz"); got != "other" {
		t.Fatalf("got %q", got)
	}
}
