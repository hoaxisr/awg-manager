package exclusions

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestManualDomainExclusions(t *testing.T) {
	m := New(Config{Domains: []string{"Example.COM.", "Mail.Other.ORG.", "", " . "}}, nil)
	tests := []struct {
		name   string
		host   string
		domain string
		want   bool
	}{
		{name: "exact host", host: "EXAMPLE.com.", domain: "example.com", want: true},
		{name: "host subdomain", host: "CDN.Example.Com.", domain: "example.com", want: true},
		{name: "exact registered domain", host: "unrelated.test", domain: "EXAMPLE.COM.", want: true},
		{name: "registered domain below exclusion", host: "unrelated.test", domain: "cdn.example.com", want: true},
		{name: "nested excluded host", host: "x.mail.other.org", domain: "other.org", want: true},
		{name: "label boundary", host: "badexample.com", domain: "badexample.com"},
		{name: "unrelated domain", host: "example.net", domain: "example.net"},
		{name: "parent host alone is not an excluded name", host: "other.org", domain: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := m.Excluded(context.Background(), tc.host, tc.domain)
			if err != nil {
				t.Fatalf("Excluded: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Excluded(%q, %q) = %v, want %v", tc.host, tc.domain, got, tc.want)
			}
		})
	}
}

func TestBroaderRegisteredDomainCoversExcludedSubdomain(t *testing.T) {
	m := New(Config{Domains: []string{"Deep.Mail.Example.Com."}}, nil)
	tests := []struct {
		name   string
		host   string
		domain string
		want   bool
	}{
		{name: "broader registered domain", host: "www.example.com", domain: "EXAMPLE.COM.", want: true},
		{name: "registered domain equals broader host", host: "example.com", domain: "example.com", want: true},
		{name: "intermediate broader domain", host: "www.mail.example.com", domain: "mail.example.com", want: true},
		{name: "narrower candidate does not cover exclusion", host: "www.example.com", domain: "www.example.com"},
		{name: "suffix without label boundary", host: "www.badexample.com", domain: "badexample.com"},
		{name: "unrelated registered domain", host: "www.example.net", domain: "example.net"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := m.Excluded(context.Background(), tc.host, tc.domain)
			if err != nil {
				t.Fatalf("Excluded: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Excluded(%q, %q) = %v, want %v", tc.host, tc.domain, got, tc.want)
			}
		})
	}
}

func TestEmptyMatcherIsNoOp(t *testing.T) {
	called := false
	callback := RuleSetMatcherFunc(func(context.Context, string, []string) (bool, error) {
		called = true
		return true, errors.New("must not be called")
	})
	matchers := map[string]*Matcher{
		"nil":           nil,
		"zero value":    {},
		"empty config":  New(Config{}, callback),
		"blank domains": New(Config{Domains: []string{"", "."}}, callback),
	}
	for name, m := range matchers {
		t.Run(name, func(t *testing.T) {
			got, err := m.Excluded(context.Background(), "WWW.Example.COM.", "example.com")
			if got || err != nil {
				t.Fatalf("Excluded = %v, %v; want false, nil", got, err)
			}
		})
	}
	if called {
		t.Fatal("rule-set matcher called without selected tags")
	}
}

type matchCall struct {
	input string
	tags  []string
}

type recordingMatcher struct {
	results map[string]bool
	err     error
	calls   []matchCall
}

func (m *recordingMatcher) MatchRuleSets(_ context.Context, input string, tags []string) (bool, error) {
	m.calls = append(m.calls, matchCall{input: input, tags: append([]string(nil), tags...)})
	if m.err != nil {
		return false, m.err
	}
	return m.results[input], nil
}

func TestRuleSetExclusions(t *testing.T) {
	t.Run("registered domain match after host miss", func(t *testing.T) {
		rm := &recordingMatcher{results: map[string]bool{"example.com": true}}
		tags := []string{"geosite-one", "remote-two"}
		m := New(Config{RuleSetTags: tags}, rm)
		tags[0] = "mutated"

		got, err := m.Excluded(context.Background(), "WWW.Example.COM.", "Example.Com.")
		if err != nil || !got {
			t.Fatalf("Excluded = %v, %v; want true, nil", got, err)
		}
		want := []matchCall{
			{input: "www.example.com", tags: []string{"geosite-one", "remote-two"}},
			{input: "example.com", tags: []string{"geosite-one", "remote-two"}},
		}
		if !reflect.DeepEqual(rm.calls, want) {
			t.Fatalf("calls = %#v, want %#v", rm.calls, want)
		}
	})

	t.Run("host match short circuits domain", func(t *testing.T) {
		rm := &recordingMatcher{results: map[string]bool{"www.example.com": true}}
		m := New(Config{RuleSetTags: []string{"selected"}}, rm)
		got, err := m.Excluded(context.Background(), "www.example.com", "example.com")
		if err != nil || !got {
			t.Fatalf("Excluded = %v, %v; want true, nil", got, err)
		}
		if len(rm.calls) != 1 || rm.calls[0].input != "www.example.com" {
			t.Fatalf("calls = %#v, want only host", rm.calls)
		}
	})

	t.Run("equal host and domain are checked once", func(t *testing.T) {
		rm := &recordingMatcher{}
		m := New(Config{RuleSetTags: []string{"selected"}}, rm)
		got, err := m.Excluded(context.Background(), "Example.COM.", "example.com")
		if err != nil || got {
			t.Fatalf("Excluded = %v, %v; want false, nil", got, err)
		}
		if len(rm.calls) != 1 || rm.calls[0].input != "example.com" {
			t.Fatalf("calls = %#v, want one normalized input", rm.calls)
		}
	})

	t.Run("manual match avoids rule-set request", func(t *testing.T) {
		rm := &recordingMatcher{err: errors.New("must not be called")}
		m := New(Config{Domains: []string{"example.com"}, RuleSetTags: []string{"selected"}}, rm)
		got, err := m.Excluded(context.Background(), "cdn.example.com", "example.com")
		if err != nil || !got {
			t.Fatalf("Excluded = %v, %v; want true, nil", got, err)
		}
		if len(rm.calls) != 0 {
			t.Fatalf("rule-set calls = %#v, want none", rm.calls)
		}
	})
}

func TestRuleSetMatcherErrors(t *testing.T) {
	t.Run("callback error is wrapped and preserves identity", func(t *testing.T) {
		wantErr := errors.New("rule-set unavailable")
		rm := &recordingMatcher{err: wantErr}
		m := New(Config{RuleSetTags: []string{"selected"}}, rm)
		got, err := m.Excluded(context.Background(), "www.example.com", "example.com")
		if got {
			t.Fatal("Excluded = true on matcher error")
		}
		if !errors.Is(err, wantErr) {
			t.Fatalf("Excluded error = %v, want wrapped %v", err, wantErr)
		}
		if len(rm.calls) != 1 {
			t.Fatalf("calls = %#v, want stop after first error", rm.calls)
		}
	})

	t.Run("selected tags require a matcher", func(t *testing.T) {
		m := New(Config{RuleSetTags: []string{"selected"}}, nil)
		got, err := m.Excluded(context.Background(), "www.example.com", "example.com")
		if got || !errors.Is(err, ErrNoRuleSetMatcher) {
			t.Fatalf("Excluded = %v, %v; want false, ErrNoRuleSetMatcher", got, err)
		}
	})

	t.Run("manual exclusion wins over missing matcher", func(t *testing.T) {
		m := New(Config{Domains: []string{"example.com"}, RuleSetTags: []string{"selected"}}, nil)
		got, err := m.Excluded(context.Background(), "www.example.com", "example.com")
		if !got || err != nil {
			t.Fatalf("Excluded = %v, %v; want true, nil", got, err)
		}
	})
}
