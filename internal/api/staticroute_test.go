package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

type recStaticRouteSvc struct {
	calls []string
	err   error
}

func (s *recStaticRouteSvc) rec(format string, a ...any) error {
	s.calls = append(s.calls, fmt.Sprintf(format, a...))
	return s.err
}
func (s *recStaticRouteSvc) List() ([]storage.StaticRouteList, error) { return nil, nil }
func (s *recStaticRouteSvc) Get(id string) (*storage.StaticRouteList, error) {
	return &storage.StaticRouteList{ID: id}, nil
}
func (s *recStaticRouteSvc) Create(_ context.Context, rl storage.StaticRouteList) (*storage.StaticRouteList, error) {
	err := s.rec("Create:%s:%s", rl.Name, rl.TunnelID)
	out := rl
	if out.ID == "" {
		out.ID = "srl-new"
	}
	return &out, err
}
func (s *recStaticRouteSvc) Update(_ context.Context, rl storage.StaticRouteList) (*storage.StaticRouteList, error) {
	err := s.rec("Update:%s:%s:%s", rl.ID, rl.Name, rl.TunnelID)
	out := rl
	if out.ID == "" {
		out.ID = "srl-new"
	}
	return &out, err
}
func (s *recStaticRouteSvc) Delete(_ context.Context, id string) error { return s.rec("Delete:%s", id) }
func (s *recStaticRouteSvc) SetEnabled(_ context.Context, id string, enabled bool) error {
	return s.rec("SetEnabled:%s:%v", id, enabled)
}
func (s *recStaticRouteSvc) Import(_ context.Context, tunnelID, name, batContent string) (*storage.StaticRouteList, error) {
	err := s.rec("Import:%s:%s:%d", tunnelID, name, len(batContent))
	return &storage.StaticRouteList{ID: "srl-new", Name: name, TunnelID: tunnelID}, err
}

// Каждая мутирующая ручка: аргументы уходят в службу без перестановок; успех публикует
// точный ключ; отказ службы — не-200 и без публикаций.
func TestStaticRouteHandler_MutationsForwardArgsAndPublish(t *testing.T) {
	const res = "routing.staticRoutes"
	cases := []struct {
		name   string
		call   func(h *StaticRouteHandler) http.HandlerFunc
		method string
		target string
		body   string
		want   []string
		pub    []string
	}{
		{"Create", func(h *StaticRouteHandler) http.HandlerFunc { return h.Create }, "POST", "/static-routes/create",
			`{"name":"Office","tunnelID":"awg3","subnets":["10.1.0.0/16"]}`,
			[]string{"Create:Office:awg3"}, []string{res + "/create"}},
		{"Update", func(h *StaticRouteHandler) http.HandlerFunc { return h.Update }, "POST", "/static-routes/update",
			`{"id":"srl7","name":"Office2","tunnelID":"awg3"}`,
			[]string{"Update:srl7:Office2:awg3"}, []string{res + "/update"}},
		{"Delete", func(h *StaticRouteHandler) http.HandlerFunc { return h.Delete }, "POST", "/static-routes/delete?id=srl7", "",
			[]string{"Delete:srl7"}, []string{res + "/delete"}},
		{"SetEnabled", func(h *StaticRouteHandler) http.HandlerFunc { return h.SetEnabled }, "POST", "/static-routes/set-enabled?id=srl7",
			`{"enabled":false}`,
			[]string{"SetEnabled:srl7:false"}, []string{res + "/set-enabled"}},
		{"Import", func(h *StaticRouteHandler) http.HandlerFunc { return h.Import }, "POST", "/static-routes/import",
			`{"tunnelID":"awg3","name":"Bat","content":"route ADD 1.2.3.0 MASK 255.255.255.0 0.0.0.0"}`,
			[]string{"Import:awg3:Bat:44"}, []string{res + "/import"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &recStaticRouteSvc{}
			h := NewStaticRouteHandler(svc, nil)
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

			fsvc := &recStaticRouteSvc{err: errors.New("io")}
			hf := NewStaticRouteHandler(fsvc, nil)
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
