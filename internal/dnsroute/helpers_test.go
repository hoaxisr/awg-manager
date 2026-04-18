package dnsroute

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// noopLogger returns a logger for tests.
func noopLogger() *logger.Logger {
	return logger.New().WithComponent("dnsroute-test")
}

// fakePoster records payloads passed to Post for assertion.
type fakePoster struct {
	mu       sync.Mutex
	payloads []any
}

func (f *fakePoster) Post(_ context.Context, payload any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.payloads = append(f.payloads, payload)
	return json.RawMessage(`{}`), nil
}

func (f *fakePoster) Payloads() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]any, len(f.payloads))
	copy(out, f.payloads)
	return out
}

// nopPublisher satisfies command.StatusPublisher without side effects.
type nopPublisher struct{}

func (nopPublisher) Publish(string, any) {}

// newTestNDMS builds real *query.Queries and *command.Commands backed by a
// FakeGetter + fakePoster. Seeds empty JSON for the two paths dnsroute
// reconcile reads so reconcile becomes a no-op (no target → empty diff).
func newTestNDMS() (*query.Queries, *command.Commands, *fakePoster, *query.FakeGetter) {
	poster := &fakePoster{}
	fg := query.NewFakeGetter()
	// dnsroute reconcile reads these two endpoints; default to empty maps
	// so List() succeeds and returns no current state.
	fg.SetJSON("/show/rc/object-group/fqdn", `{}`)
	fg.SetJSON("/show/rc/dns-proxy/route", `{}`)

	q := query.NewQueries(query.Deps{
		Getter: fg,
		Logger: query.NopLogger(),
		IsOS5:  func() bool { return true },
	})
	sc := command.NewSaveCoordinator(poster, nopPublisher{}, 500*time.Millisecond, 5*time.Second)
	c := command.NewCommands(command.Deps{
		Poster:  poster,
		Save:    sc,
		Queries: q,
		IsOS5:   func() bool { return true },
	})
	return q, c, poster, fg
}
