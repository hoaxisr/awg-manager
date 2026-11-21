package main

import (
	"github.com/p1neappleXpress/OpenFlux/transport"
	"reflect"
	"testing"
)

func TestExpandDebugFlags(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"openflux", "-d"}, []string{"openflux", "--debug=1"}},
		{[]string{"openflux", "-dd"}, []string{"openflux", "--debug=2"}},
		{[]string{"openflux", "-ddd"}, []string{"openflux", "--debug=3"}},
		{[]string{"openflux", "-dddd"}, []string{"openflux", "--debug=3"}},
		{[]string{"openflux", "-d=2"}, []string{"openflux", "--debug=2"}},
		{[]string{"openflux", "--debug=2"}, []string{"openflux", "--debug=2"}},
		{[]string{"openflux", "--debug", "3"}, []string{"openflux", "--debug", "3"}},
		{[]string{"openflux", "--debug", "-r=exit"}, []string{"openflux", "--debug=1", "--role=exit"}},
		{[]string{"openflux", "--debug"}, []string{"openflux", "--debug=1"}},
		// Single-dash long flags that start with d are not debug flags.
		{[]string{"openflux", "-direct-listen=:9000"}, []string{"openflux", "-direct-listen=:9000"}},
		{[]string{"openflux", "-direct-dial", "1.2.3.4:9000"}, []string{"openflux", "-direct-dial", "1.2.3.4:9000"}},
	}
	for _, c := range cases {
		if got := expandShortFlags(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("expandShortFlags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPickSessionContext(t *testing.T) {
	specs := []transportSpec{
		{Name: "direct", Type: "direct", Priority: 100},
		{Name: "vyandex", Type: "vyandex", Priority: 30, URL: "https://volga/doc"},
		{Name: "yandex", Type: "yandex", Priority: 50, URL: "https://docs/doc"},
	}
	cases := []struct {
		name, explicit, url string
		specs               []transportSpec
		want                string
	}{
		{"explicit wins", "ctx", "https://u", specs, "ctx"},
		// The context no longer encodes the channel: without an explicit
		// context (shared via the openflux:// link) every setup collapses to
		// the one fixed placeholder, so URLs, transport priorities and
		// cupsonline room lists can no longer make two peers derive different
		// contexts and fail the handshake. See transport.KDFContexts.
		{"no explicit: --url ignored", "", "https://u", specs, "http://#"},
		{"no explicit: transport URLs ignored", "", "http://#", specs, "http://#"},
		{"no URL anywhere", "", "http://#", specs[:1], "http://#"},
		{"cupsonline rooms never a context", "", "http://#", []transportSpec{
			{Type: "cupsonline", Priority: 100, URL: "WyIxYzE0NGQwZS1lMDQw"},
			{Type: "yandex", Priority: 50, URL: "https://docs/doc"},
		}, "http://#"},
		{"classic cupsonline client with rooms as --url", "", "WyIxYzE0NGQwZS1lMDQw",
			[]transportSpec{{Type: "cupsonline", Priority: 100, URL: "WyIxYzE0NGQwZS1lMDQw"}}, "http://#"},
	}
	for _, c := range cases {
		if got, _ := transport.KDFContexts(c.explicit, c.url, contextSources(c.specs)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
