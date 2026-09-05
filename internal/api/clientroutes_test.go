package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/clientroute"
)

type recClientRouteSvc struct {
	calls []string
	err   error
}

func (s *recClientRouteSvc) rec(format string, a ...any) error {
	s.calls = append(s.calls, fmt.Sprintf(format, a...))
	return s.err
}
func (s *recClientRouteSvc) List() ([]clientroute.ClientRoute, error) { return nil, nil }
func (s *recClientRouteSvc) Create(_ context.Context, r clientroute.ClientRoute) (*clientroute.ClientRoute, error) {
	err := s.rec("Create:%s:%s:%s", r.ClientIP, r.TunnelID, r.Fallback)
	out := r
	if out.ID == "" {
		out.ID = "cr-new"
	}
	return &out, err
}
func (s *recClientRouteSvc) Update(_ context.Context, r clientroute.ClientRoute) (*clientroute.ClientRoute, error) {
	err := s.rec("Update:%s:%s:%s:%s", r.ID, r.ClientIP, r.TunnelID, r.Fallback)
	out := r
	if out.ID == "" {
		out.ID = "cr-new"
	}
	return &out, err
}
func (s *recClientRouteSvc) Delete(_ context.Context, id string) error { return s.rec("Delete:%s", id) }
func (s *recClientRouteSvc) SetEnabled(_ context.Context, id string, enabled bool) error {
	return s.rec("SetEnabled:%s:%v", id, enabled)
}
func (s *recClientRouteSvc) OnTunnelStart(context.Context, string, string) error { return nil }
func (s *recClientRouteSvc) OnTunnelStop(context.Context, string) error          { return nil }
func (s *recClientRouteSvc) OnTunnelDelete(context.Context, string) error        { return nil }
func (s *recClientRouteSvc) Reconcile(context.Context, map[string]string) error  { return nil }
func (s *recClientRouteSvc) CleanupAll(context.Context) error                    { return nil }

// Каждая мутирующая ручка: аргументы уходят в службу без перестановок; успех публикует
// точный ключ; отказ службы — не-200 и без публикаций.
func TestClientRouteHandler_MutationsForwardArgsAndPublish(t *testing.T) {
	const res = "routing.clientRoutes"
	cases := []struct {
		name   string
		call   func(h *ClientRouteHandler) http.HandlerFunc
		method string
		target string
		body   string
		want   []string
		pub    []string
	}{
		{"HandleCreate", func(h *ClientRouteHandler) http.HandlerFunc { return h.HandleCreate }, "POST", "/client-routes/create",
			`{"clientIp":"192.168.1.50","tunnelId":"awg3","fallback":"drop","enabled":true}`,
			[]string{"Create:192.168.1.50:awg3:drop"}, []string{res + "/create"}},
		{"HandleUpdate", func(h *ClientRouteHandler) http.HandlerFunc { return h.HandleUpdate }, "POST", "/client-routes/update?id=cr-1",
			`{"clientIp":"192.168.1.50","tunnelId":"awg3","fallback":"drop","enabled":true}`,
			[]string{"Update:cr-1:192.168.1.50:awg3:drop"}, []string{res + "/update"}},
		{"HandleDelete", func(h *ClientRouteHandler) http.HandlerFunc { return h.HandleDelete }, "POST", "/client-routes/delete?id=cr-1", "",
			[]string{"Delete:cr-1"}, []string{res + "/delete"}},
		{"HandleToggle", func(h *ClientRouteHandler) http.HandlerFunc { return h.HandleToggle }, "POST", "/client-routes/toggle?id=cr-1",
			`{"enabled":false}`,
			[]string{"SetEnabled:cr-1:false"}, []string{res + "/toggle"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &recClientRouteSvc{}
			h := NewClientRouteHandler(svc)
			p := newBusProbe(t)
			h.SetEventBus(p.bus())
			rr := perform(tc.call(h), tc.method, tc.target, tc.body)
			if rr.Code != 200 {
				t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
			}
			if !reflect.DeepEqual(svc.calls, tc.want) {
				t.Fatalf("служба: %v, want %v", svc.calls, tc.want)
			}
			if got := p.invalidated(); !reflect.DeepEqual(got, tc.pub) {
				t.Fatalf("публикации: %v, want %v", got, tc.pub)
			}

			fsvc := &recClientRouteSvc{err: errors.New("storage")}
			hf := NewClientRouteHandler(fsvc)
			pf := newBusProbe(t)
			hf.SetEventBus(pf.bus())
			if rr := perform(tc.call(hf), tc.method, tc.target, tc.body); rr.Code == 200 {
				t.Fatalf("отказ службы обязан быть не-200: %s", rr.Body.String())
			}
			if got := pf.invalidated(); len(got) != 0 {
				t.Fatalf("на отказе публикаций быть не должно: %v", got)
			}
		})
	}
}
