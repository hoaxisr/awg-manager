package singbox

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOperator_ListTunnels_NoConfig(t *testing.T) {
	dir := t.TempDir()
	op := NewOperator(OperatorDeps{
		Dir: dir,
	})
	list, err := op.ListTunnels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty, got %d", len(list))
	}
}

func TestOperator_ConfigPaths(t *testing.T) {
	dir := t.TempDir()
	op := NewOperator(OperatorDeps{Dir: dir})
	if op.configPath != filepath.Join(dir, "config.json") {
		t.Errorf("configPath: %s", op.configPath)
	}
	if op.pidPath != filepath.Join(dir, "sing-box.pid") {
		t.Errorf("pidPath: %s", op.pidPath)
	}
}

func TestParseProxyIdx(t *testing.T) {
	cases := []struct {
		in      string
		wantIdx int
		wantErr bool
	}{
		{"Proxy0", 0, false},
		{"Proxy42", 42, false},
		{"", 0, true},
		{"Proxy", 0, true},
		{"WrongPrefix0", 0, true},
		{"Proxy-1", -1, false}, // Sscanf accepts negative — that's OK, semantic validation elsewhere
	}
	for _, c := range cases {
		got, err := parseProxyIdx(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err=%v wantErr=%v", c.in, err, c.wantErr)
		}
		if err == nil && got != c.wantIdx {
			t.Errorf("%q: got=%d want=%d", c.in, got, c.wantIdx)
		}
	}
}
