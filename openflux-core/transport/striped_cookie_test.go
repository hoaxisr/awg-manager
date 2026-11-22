package transport

import (
	"sync"
	"testing"
)

type stubCookieTransport struct {
	*BaseTransport
	mu  sync.Mutex
	jar map[string]string
}

func newStubCookieTransport(initial map[string]string) *stubCookieTransport {
	cp := make(map[string]string, len(initial))
	for k, v := range initial {
		cp[k] = v
	}
	return &stubCookieTransport{
		BaseTransport: NewBaseTransport(DefaultConfig()),
		jar:           cp,
	}
}

func (s *stubCookieTransport) Send([]byte) error { return nil }

func (s *stubCookieTransport) FetchCookies() (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make(map[string]string, len(s.jar))
	for k, v := range s.jar {
		cp[k] = v
	}
	return cp, nil
}

func (s *stubCookieTransport) ApplyCookies(jar map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make(map[string]string, len(jar))
	for k, v := range jar {
		cp[k] = v
	}
	s.jar = cp
	return nil
}

func TestStripedAndBatchedCookiePropagationAllLanes(t *testing.T) {
	lane0 := newStubCookieTransport(nil)
	lane1 := newStubCookieTransport(nil)

	b0 := NewBatchedTransport(lane0, 64<<10)
	b1 := NewBatchedTransport(lane1, 64<<10)

	st := NewStripedTransport([]Transport{b0, b1})
	if err := st.ApplyCookies(map[string]string{"Mpop": "tok1", "sdc": "val1"}); err != nil {
		t.Fatalf("StripedTransport.ApplyCookies failed: %v", err)
	}

	for i, lane := range []*stubCookieTransport{lane0, lane1} {
		got, err := lane.FetchCookies()
		if err != nil {
			t.Fatalf("lane %d FetchCookies failed: %v", i, err)
		}
		if got["Mpop"] != "tok1" || got["sdc"] != "val1" {
			t.Fatalf("lane %d missing applied cookies: %+v", i, got)
		}
	}

	fetched, err := st.FetchCookies()
	if err != nil {
		t.Fatalf("StripedTransport.FetchCookies failed: %v", err)
	}
	if fetched["Mpop"] != "tok1" || fetched["sdc"] != "val1" {
		t.Fatalf("StripedTransport.FetchCookies mismatch: %+v", fetched)
	}
}
