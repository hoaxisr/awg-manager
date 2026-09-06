package accesspolicy

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// fakeLifecycle records Start/Stop calls by tunnel ID.
type fakeLifecycle struct {
	started []string
	stopped []string
	err     error
}

func (f *fakeLifecycle) Start(_ context.Context, id string) error {
	f.started = append(f.started, id)
	return f.err
}

func (f *fakeLifecycle) Stop(_ context.Context, id string) error {
	f.stopped = append(f.stopped, id)
	return f.err
}

// fakeResolver maps NDMS interface names to managed tunnel IDs.
type fakeResolver struct{ m map[string]string }

func (f *fakeResolver) ManagedTunnelByNDMSName(_ context.Context, ndmsName string) (string, bool) {
	id, ok := f.m[ndmsName]
	return id, ok
}

// Managed tunnel + up=true must route to the lifecycle Start (orchestrator),
// NOT a raw NDMS interface flip. A bare flip leaves NativeWG without its kmod
// proxy / endpoint rewrite and the handshake never completes (issue #183).
// interfaces is nil here on purpose: if the code fell through to the raw flip
// it would panic, proving the managed path bypasses it.
func TestSetInterfaceUp_ManagedTunnel_Up_RoutesToLifecycleStart(t *testing.T) {
	lc := &fakeLifecycle{}
	s := &ServiceImpl{
		lifecycle:      lc,
		tunnelResolver: &fakeResolver{m: map[string]string{"Wireguard4": "awg10"}},
	}

	if err := s.SetInterfaceUp(context.Background(), "Wireguard4", true); err != nil {
		t.Fatalf("SetInterfaceUp: %v", err)
	}
	if len(lc.started) != 1 || lc.started[0] != "awg10" {
		t.Errorf("lifecycle.Start calls = %v, want [awg10]", lc.started)
	}
	if len(lc.stopped) != 0 {
		t.Errorf("lifecycle.Stop calls = %v, want none", lc.stopped)
	}
}

// Managed tunnel + up=false must route to lifecycle Stop (full teardown incl.
// kmod removal + PersistStopped), not a raw NDMS down that diverges state.
func TestSetInterfaceUp_ManagedTunnel_Down_RoutesToLifecycleStop(t *testing.T) {
	lc := &fakeLifecycle{}
	s := &ServiceImpl{
		lifecycle:      lc,
		tunnelResolver: &fakeResolver{m: map[string]string{"Wireguard4": "awg10"}},
	}

	if err := s.SetInterfaceUp(context.Background(), "Wireguard4", false); err != nil {
		t.Fatalf("SetInterfaceUp: %v", err)
	}
	if len(lc.stopped) != 1 || lc.stopped[0] != "awg10" {
		t.Errorf("lifecycle.Stop calls = %v, want [awg10]", lc.stopped)
	}
	if len(lc.started) != 0 {
		t.Errorf("lifecycle.Start calls = %v, want none", lc.started)
	}
}

type recPoster struct{ posts []string }

func (p *recPoster) Post(_ context.Context, payload any) (json.RawMessage, error) {
	b, _ := json.Marshal(payload)
	p.posts = append(p.posts, string(b))
	return json.RawMessage(`{}`), nil
}

func newRawFlipService(t *testing.T) (*ServiceImpl, *recPoster) {
	t.Helper()
	poster := &recPoster{}
	// time.Hour: Request() ставит AfterFunc-таймер, и 500 мс под -race допишут save-POST в
	// posts гонкой с чтением в тесте (форма ops/operator_os5_lifecycle_test.go:31).
	sc := command.NewSaveCoordinator(poster, nil, time.Hour, time.Hour, 0, nil)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return &ServiceImpl{
		interfaces:     command.NewInterfaceCommands(poster, sc, q, nil),
		lifecycle:      &fakeLifecycle{},
		tunnelResolver: &fakeResolver{m: map[string]string{}}, // ничего не managed
	}, poster
}

// Не-managed интерфейс: lifecycle не трогается, в NDMS уходит сырой флип up/down.
// Прежний тест глушил nil-панику recover'ом и до ассертов не доходил.
func TestSetInterfaceUp_NonManaged_RawFlipWithoutLifecycle(t *testing.T) {
	for _, tc := range []struct {
		up   bool
		want string
	}{
		{true, `{"interface":{"GigabitEthernet1":{"up":true}}}`},
		{false, `{"interface":{"GigabitEthernet1":{"up":false}}}`},
	} {
		s, poster := newRawFlipService(t)
		if err := s.SetInterfaceUp(context.Background(), "GigabitEthernet1", tc.up); err != nil {
			t.Fatalf("up=%v: %v", tc.up, err)
		}
		lc := s.lifecycle.(*fakeLifecycle)
		if len(lc.started) != 0 || len(lc.stopped) != 0 {
			t.Fatalf("lifecycle тронут: %v %v", lc.started, lc.stopped)
		}
		if !reflect.DeepEqual(poster.posts, []string{tc.want}) {
			t.Fatalf("RCI = %v, want [%s]", poster.posts, tc.want)
		}
	}
}
