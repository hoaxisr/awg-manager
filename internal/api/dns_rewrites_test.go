package api

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/dnsrewrite"
)

type recDNSRewritesSvc struct {
	calls []string
	err   error
}

func (s *recDNSRewritesSvc) rec(format string, a ...any) error {
	s.calls = append(s.calls, fmt.Sprintf(format, a...))
	return s.err
}
func (s *recDNSRewritesSvc) List() ([]dnsrewrite.DNSRewrite, error) {
	return []dnsrewrite.DNSRewrite{}, nil
}
func (s *recDNSRewritesSvc) Add(rw dnsrewrite.DNSRewrite) error {
	return s.rec("Add:%s:%v", rw.Pattern, rw.IPs)
}
func (s *recDNSRewritesSvc) Update(idx int, rw dnsrewrite.DNSRewrite) error {
	return s.rec("Update:%d:%s", idx, rw.Pattern)
}
func (s *recDNSRewritesSvc) Delete(idx int) error    { return s.rec("Delete:%d", idx) }
func (s *recDNSRewritesSvc) Move(from, to int) error { return s.rec("Move:%d:%d", from, to) }

// У DNSRewritesHandler нет SetEventBus и публикаций (dns_rewrites.go:57-60) — зонд не
// подключаем.

func TestDNSRewritesHandler_Move(t *testing.T) {
	svc := &recDNSRewritesSvc{}
	h := NewDNSRewritesHandler(svc, nil)
	rr := perform(h.Move, "POST", "/singbox/router/dns/rewrites/move", `{"from":2,"to":0}`)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if want := []string{"Move:2:0"}; !reflect.DeepEqual(svc.calls, want) {
		t.Fatalf("служба: %v, want %v", svc.calls, want)
	}
	data, _ := decodeJSONBody(t, rr)["data"].(map[string]any)
	if data["ok"] != true {
		t.Fatalf("body: %s", rr.Body.String())
	}
}

func TestDNSRewritesHandler_Move_ServiceFailureIsBadRequest(t *testing.T) {
	svc := &recDNSRewritesSvc{err: errors.New("index out of range")}
	h := NewDNSRewritesHandler(svc, nil)
	rr := perform(h.Move, "POST", "/singbox/router/dns/rewrites/move", `{"from":2,"to":0}`)
	if rr.Code != 400 {
		t.Fatalf("code=%d, want 400: %s", rr.Code, rr.Body.String())
	}
	if got := decodeJSONBody(t, rr)["code"]; got != "BAD_REQUEST" {
		t.Fatalf("code=%v, want BAD_REQUEST", got)
	}
	if want := []string{"Move:2:0"}; !reflect.DeepEqual(svc.calls, want) {
		t.Fatalf("служба всё равно должна была вызваться (проверка ошибки после вызова): %v, want %v", svc.calls, want)
	}
}

func TestDNSRewritesHandler_Delete(t *testing.T) {
	svc := &recDNSRewritesSvc{}
	h := NewDNSRewritesHandler(svc, nil)
	rr := perform(h.Delete, "POST", "/singbox/router/dns/rewrites/delete", `{"index":3}`)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if want := []string{"Delete:3"}; !reflect.DeepEqual(svc.calls, want) {
		t.Fatalf("служба: %v, want %v", svc.calls, want)
	}
}

func TestDNSRewritesHandler_Update(t *testing.T) {
	svc := &recDNSRewritesSvc{}
	h := NewDNSRewritesHandler(svc, nil)
	rr := perform(h.Update, "POST", "/singbox/router/dns/rewrites/update",
		`{"index":1,"rewrite":{"pattern":"*.corp.local","ips":["10.0.0.5"]}}`)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if want := []string{"Update:1:*.corp.local"}; !reflect.DeepEqual(svc.calls, want) {
		t.Fatalf("служба: %v, want %v", svc.calls, want)
	}
}
