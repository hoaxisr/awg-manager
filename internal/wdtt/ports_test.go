package wdtt

import "testing"

func TestDefaultRawListenAddr(t *testing.T) {
	got := defaultRawListenAddr("0.0.0.0:56002")
	if got != "0.0.0.0:56003" {
		t.Fatalf("got %q", got)
	}
}

func TestServerLinkListenPort(t *testing.T) {
	wg := ServerConfig{Listen: "0.0.0.0:56002", WgPort: 56001, RelayMode: ConnModeWG}
	if p := wg.LinkListenPort(); p != 56002 {
		t.Fatalf("wg port: got %d", p)
	}
	wgDirect := ServerConfig{
		Listen:       "0.0.0.0:56010",
		DirectListen: "0.0.0.0:56002",
		WgPort:       56011,
		RelayMode:    ConnModeWG,
	}
	if p := wgDirect.LinkListenPort(); p != 56002 {
		t.Fatalf("wg direct port: got %d", p)
	}
	raw := ServerConfig{Listen: "0.0.0.0:56002", RelayMode: ConnModeRaw, RawListen: "0.0.0.0:56003"}
	if p := raw.LinkListenPort(); p != 56003 {
		t.Fatalf("raw port: got %d", p)
	}
	rawAuto := ServerConfig{Listen: "0.0.0.0:56002", RelayMode: ConnModeRaw}
	if p := rawAuto.LinkListenPort(); p != 56003 {
		t.Fatalf("raw auto port: got %d", p)
	}
}

func TestBuildServerArgsDualListen(t *testing.T) {
	got := buildServerArgs(ServerConfig{
		Listen:       "0.0.0.0:56010",
		DirectListen: "0.0.0.0:56002",
		WgPort:       56011,
		Password:     "secret",
		RelayMode:    ConnModeWG,
	})
	assertHasFlagPair(t, got, "-listen-raw", "0.0.0.0:56011")
	assertHasFlagPair(t, got, "-listen-direct", "0.0.0.0:56002")
}

func TestBuildServerArgsDirectSameAsDTLS(t *testing.T) {
	got := buildServerArgs(ServerConfig{
		Listen:       "0.0.0.0:56002",
		DirectListen: "0.0.0.0:56002",
		WgPort:       56001,
		Password:     "secret",
	})
	for i := 0; i < len(got)-1; i++ {
		if got[i] == "-listen-direct" {
			t.Fatalf("must not pass -listen-direct when same as -listen: %v", got)
		}
	}
}

func assertHasFlagPair(t *testing.T, got []string, flag, val string) {
	t.Helper()
	for j := 0; j < len(got)-1; j++ {
		if got[j] == flag && got[j+1] == val {
			return
		}
	}
	t.Fatalf("missing %q %q in %v", flag, val, got)
}

func TestBuildServerArgsRawListen(t *testing.T) {
	got := buildServerArgs(ServerConfig{
		Listen:    "0.0.0.0:56002",
		WgPort:    56001,
		Password:  "secret",
		RelayMode: ConnModeRaw,
		RawListen: "0.0.0.0:56003",
	})
	assertHasFlagPair(t, got, "-listen-raw", "0.0.0.0:56003")
	for _, bad := range []string{"-relay-mode", "raw"} {
		for _, g := range got {
			if g == bad {
				t.Fatalf("unexpected %q in args: %v", bad, got)
			}
		}
	}
}
