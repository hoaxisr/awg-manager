package firc

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestFixRemapListenAddr(t *testing.T) {
	cases := []struct {
		name, conf string
		want       bool
	}{
		{"remap default on + loopback", "app:\n  dnsProxy:\n    host:\n      address: 127.0.0.1\n      port: 3553\n", true},
		{"remap explicit on + ::1", "app:\n  dnsProxy:\n    host:\n      address: '[::1]'\n    disableRemap53: false\n", true},
		{"remap off", "app:\n  dnsProxy:\n    host:\n      address: 127.0.0.1\n    disableRemap53: true\n", false},
		{"wildcard already", "app:\n  dnsProxy:\n    host:\n      address: 0.0.0.0\n", false},
		{"no address", "app:\n  dnsProxy:\n    host:\n      port: 3553\n", false},
		{"no dnsProxy", "app:\n  link: [br0]\n", false},
	}
	for _, c := range cases {
		out, changed, err := fixRemapListenAddr([]byte(c.conf))
		if err != nil || changed != c.want {
			t.Fatalf("%s: changed=%v err=%v", c.name, changed, err)
		}
		if !changed {
			continue
		}
		var got struct {
			App struct {
				DNSProxy struct {
					Host struct {
						Address string `yaml:"address"`
						Port    int    `yaml:"port"`
					} `yaml:"host"`
				} `yaml:"dnsProxy"`
			} `yaml:"app"`
		}
		if err := yaml.Unmarshal(out, &got); err != nil {
			t.Fatalf("%s: reparse: %v\n%s", c.name, err, out)
		}
		if got.App.DNSProxy.Host.Address != "[::]" {
			t.Fatalf("%s: address=%q\n%s", c.name, got.App.DNSProxy.Host.Address, out)
		}
		if strings.Contains(c.conf, "port: 3553") && got.App.DNSProxy.Host.Port != 3553 {
			t.Fatalf("%s: port lost\n%s", c.name, out)
		}
	}
}
