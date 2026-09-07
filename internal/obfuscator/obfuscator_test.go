package obfuscator

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestValidate(t *testing.T) {
	ok := storage.Obfuscator{Flavor: "phobos", Target: "vpn.example.com:51824", Key: "k", Masking: "MEDIA", MaxDummy: 4, ObfuscateBytes: 16}
	if err := Validate(&ok); err != nil {
		t.Fatal(err)
	}
	// `[` внутри значения не ломает INI (заголовок секции — только с начала
	// строки), а канонический IPv6-target без него не записать.
	for name, o := range map[string]storage.Obfuscator{
		"ipv6 target":   {Flavor: "phobos", Target: "[2001:db8::1]:51824", Key: "k", Masking: "STUN"},
		"bracket в key": {Flavor: "phobos", Target: "h:1", Key: "a[b", Masking: "STUN"},
	} {
		if err := Validate(&o); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	cases := map[string]storage.Obfuscator{
		"no target":         {Flavor: "phobos", Key: "k", Masking: "STUN"},
		"bad target":        {Flavor: "phobos", Target: "host", Key: "k", Masking: "STUN"},
		"loopback target":   {Flavor: "phobos", Target: "127.0.0.1:1", Key: "k", Masking: "STUN"},
		"no key":            {Flavor: "phobos", Target: "h:1", Masking: "STUN"},
		"bad masking":       {Flavor: "phobos", Target: "h:1", Key: "k", Masking: "TLS"},
		"media on clusterm": {Flavor: "clusterm", Target: "h:1", Key: "k", Masking: "MEDIA"},
		"obfbytes clusterm": {Flavor: "clusterm", Target: "h:1", Key: "k", Masking: "STUN", ObfuscateBytes: 16},
		"dummy too big":     {Flavor: "phobos", Target: "h:1", Key: "k", Masking: "STUN", MaxDummy: 1025},
		"bad flavor":        {Flavor: "x", Target: "h:1", Key: "k", Masking: "STUN"},
		"negative idle":     {Flavor: "phobos", Target: "h:1", Key: "k", Masking: "STUN", IdleTimeout: -1},
		// Key/Target попадают в INI релея как есть: перевод строки или `[`
		// подменили бы соседние ключи и секции (достижимо через JSON API/MCP).
		"newline in key":    {Flavor: "phobos", Target: "h:1", Key: "k\nsource-if = 0.0.0.0", Masking: "STUN"},
		"cr in key":         {Flavor: "phobos", Target: "h:1", Key: "k\rx", Masking: "STUN"},
		"newline in target": {Flavor: "phobos", Target: "h:1\nkey = zzz", Key: "k", Masking: "STUN"},
		"localhost target":  {Flavor: "phobos", Target: "localhost:51824", Key: "k", Masking: "STUN"},
	}
	for name, c := range cases {
		if err := Validate(&c); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if Validate(nil) != nil {
		t.Fatal("nil is valid (no obfuscator)")
	}
}

func TestEqual(t *testing.T) {
	a := &storage.Obfuscator{Target: "h:1", Key: "k"}
	b := &storage.Obfuscator{Target: "h:1", Key: "k"}
	if !Equal(a, b) || !Equal(nil, nil) || Equal(a, nil) {
		t.Fatal("Equal")
	}
	b.Key = "z"
	if Equal(a, b) {
		t.Fatal("must differ")
	}
}

func TestBinaryName(t *testing.T) {
	if BinaryName("phobos") != "awgm-wg-obfuscator-phobos" || BinaryName("clusterm") != "awgm-wg-obfuscator-clusterm" {
		t.Fatal("names")
	}
}
