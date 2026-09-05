package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type recAccessPolicySvc struct {
	calls []string
	err   error
}

func (s *recAccessPolicySvc) rec(format string, a ...any) error {
	s.calls = append(s.calls, fmt.Sprintf(format, a...))
	return s.err
}
func (s *recAccessPolicySvc) List(context.Context) ([]accesspolicy.Policy, error) { return nil, nil }
func (s *recAccessPolicySvc) Create(_ context.Context, d string) (*accesspolicy.Policy, error) {
	return &accesspolicy.Policy{}, s.rec("Create:%s", d)
}
func (s *recAccessPolicySvc) Delete(_ context.Context, n string) error { return s.rec("Delete:%s", n) }
func (s *recAccessPolicySvc) SetDescription(_ context.Context, n, d string) error {
	return s.rec("SetDescription:%s:%s", n, d)
}
func (s *recAccessPolicySvc) SetStandalone(_ context.Context, n string, on bool) error {
	return s.rec("SetStandalone:%s:%v", n, on)
}
func (s *recAccessPolicySvc) PermitInterface(_ context.Context, n, i string, o int) error {
	return s.rec("PermitInterface:%s:%s:%d", n, i, o)
}
func (s *recAccessPolicySvc) DenyInterface(_ context.Context, n, i string) error {
	return s.rec("DenyInterface:%s:%s", n, i)
}
func (s *recAccessPolicySvc) AssignDevice(_ context.Context, mac, p string) error {
	return s.rec("AssignDevice:%s:%s", mac, p)
}
func (s *recAccessPolicySvc) UnassignDevice(_ context.Context, mac string) error {
	return s.rec("UnassignDevice:%s", mac)
}
func (s *recAccessPolicySvc) ListDevices(context.Context) ([]accesspolicy.Device, error) {
	return nil, nil
}
func (s *recAccessPolicySvc) ListGlobalInterfaces(context.Context) ([]accesspolicy.GlobalInterface, error) {
	return nil, nil
}
func (s *recAccessPolicySvc) SetInterfaceUp(_ context.Context, n string, up bool) error {
	return s.rec("SetInterfaceUp:%s:%v", n, up)
}
func (s *recAccessPolicySvc) GetPolicyMark(context.Context, string) (string, error) { return "", nil }
func (s *recAccessPolicySvc) ListPolicyExits(context.Context, string) ([]query.PolicyDefaultExit, error) {
	return nil, nil
}

// Каждая мутирующая ручка: аргументы уходят в службу без перестановок; успех публикует
// точный набор ключей; отказ службы — не-200 и без публикаций.
func TestAccessPolicyHandler_MutationsForwardArgsAndPublish(t *testing.T) {
	const pol, dev = "routing.accessPolicies", "routing.policyDevices"
	cases := []struct {
		name   string
		call   func(h *AccessPolicyHandler) http.HandlerFunc
		method string
		target string
		body   string
		want   []string // журнал службы
		pub    []string // публикации на успехе
	}{
		{"Create", func(h *AccessPolicyHandler) http.HandlerFunc { return h.Create }, "POST", "/access-policies", `{"description":"Kids"}`,
			[]string{"Create:Kids"}, []string{pol + "/create"}},
		{"Delete", func(h *AccessPolicyHandler) http.HandlerFunc { return h.Delete }, "DELETE", "/access-policies?name=Policy3", "",
			[]string{"Delete:Policy3"}, []string{pol + "/delete"}},
		{"SetDescription", func(h *AccessPolicyHandler) http.HandlerFunc { return h.SetDescription }, "POST", "/access-policies/description", `{"name":"Policy3","description":"TV"}`,
			[]string{"SetDescription:Policy3:TV"}, []string{pol + "/set-description"}},
		{"SetStandalone", func(h *AccessPolicyHandler) http.HandlerFunc { return h.SetStandalone }, "POST", "/access-policies/standalone", `{"name":"Policy3","enabled":true}`,
			[]string{"SetStandalone:Policy3:true"}, []string{pol + "/set-standalone"}},
		{"PermitInterface", func(h *AccessPolicyHandler) http.HandlerFunc { return h.PermitInterface }, "POST", "/access-policies/interface", `{"name":"Policy3","interface":"Wireguard2","order":5}`,
			[]string{"PermitInterface:Policy3:Wireguard2:5"}, []string{pol + "/permit-interface"}},
		{"DenyInterface", func(h *AccessPolicyHandler) http.HandlerFunc { return h.PermitInterface }, "DELETE", "/access-policies/interface?name=Policy3&interface=Wireguard2", "",
			[]string{"DenyInterface:Policy3:Wireguard2"}, []string{pol + "/deny-interface"}},
		{"AssignDevice", func(h *AccessPolicyHandler) http.HandlerFunc { return h.AssignDevice }, "POST", "/access-policies/device", `{"mac":"aa:bb:cc:dd:ee:01","policy":"Policy3"}`,
			[]string{"AssignDevice:aa:bb:cc:dd:ee:01:Policy3"}, []string{pol + "/assign-device", dev + "/assign-device"}},
		{"UnassignDevice", func(h *AccessPolicyHandler) http.HandlerFunc { return h.AssignDevice }, "DELETE", "/access-policies/device?mac=aa:bb:cc:dd:ee:01", "",
			[]string{"UnassignDevice:aa:bb:cc:dd:ee:01"}, []string{pol + "/unassign-device", dev + "/unassign-device"}},
		{"SetInterfaceUp", func(h *AccessPolicyHandler) http.HandlerFunc { return h.SetInterfaceUp }, "POST", "/access-policies/interface-up", `{"name":"Wireguard2","up":false}`,
			[]string{"SetInterfaceUp:Wireguard2:false"}, []string{"routing.policyInterfaces/set-interface-up", "routing.tunnels/set-interface-up", "tunnels/set-interface-up"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &recAccessPolicySvc{}
			h := NewAccessPolicyHandler(svc)
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

			fsvc := &recAccessPolicySvc{err: errors.New("ndms")}
			hf := NewAccessPolicyHandler(fsvc)
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

func TestAccessPolicyHandler_AssignDevice_RequiresMACAndPolicy(t *testing.T) {
	svc := &recAccessPolicySvc{}
	h := NewAccessPolicyHandler(svc)
	if rr := perform(h.AssignDevice, "POST", "/access-policies/device", `{"policy":"Policy3"}`); decodeJSONBody(t, rr)["code"] != "MISSING_MAC" {
		t.Fatal(rr.Body.String())
	}
	if rr := perform(h.AssignDevice, "POST", "/access-policies/device", `{"mac":"aa:bb:cc:dd:ee:01"}`); decodeJSONBody(t, rr)["code"] != "MISSING_POLICY" {
		t.Fatal(rr.Body.String())
	}
	if len(svc.calls) != 0 {
		t.Fatalf("служба не должна вызываться: %v", svc.calls)
	}
}
