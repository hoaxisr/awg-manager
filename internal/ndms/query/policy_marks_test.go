// internal/ndms/query/policy_marks_test.go
package query

import (
	"context"
	"errors"
	"testing"
)

func TestPolicyMarkStore_Found(t *testing.T) {
	// /show/ip/policy returns a top-level map of policy-name → policy-object
	// (NOT wrapped in {"policy": {...}}). Verified on hardware:
	//   curl http://127.0.0.1:79/rci/show/ip/policy
	//   {"Policy0":{"description":"IoT_VPN","mark":"ffffaaa","table4":4096,...},
	//    "Policy1":{"description":"Only_Letai","mark":"ffffaab","table4":4098,...}}
	fg := NewFakeGetter()
	fg.SetRaw("/show/ip/policy", []byte(`{"Policy0":{"description":"IoT_VPN","mark":"ffffaaa"},"Policy1":{"description":"Only_Letai","mark":"ffffaab"}}`))
	s := NewPolicyMarkStore(fg, NopLogger())

	mark, err := s.Get(context.Background(), "Policy0")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if mark != "0xffffaaa" {
		t.Errorf("want 0xffffaaa, got %q", mark)
	}
}

func TestPolicyMarkStore_NotFound(t *testing.T) {
	fg := NewFakeGetter()
	fg.SetRaw("/show/ip/policy", []byte(`{"Policy0":{"mark":"ffffaaa"}}`))
	s := NewPolicyMarkStore(fg, NopLogger())

	_, err := s.Get(context.Background(), "PolicyMissing")
	if !errors.Is(err, ErrPolicyMarkNotFound) {
		t.Errorf("expected ErrPolicyMarkNotFound, got %v", err)
	}
}

func TestPolicyMarkStore_EmptyMark(t *testing.T) {
	fg := NewFakeGetter()
	fg.SetRaw("/show/ip/policy", []byte(`{"Policy0":{"mark":""}}`))
	s := NewPolicyMarkStore(fg, NopLogger())

	_, err := s.Get(context.Background(), "Policy0")
	if !errors.Is(err, ErrPolicyMarkNotFound) {
		t.Errorf("expected ErrPolicyMarkNotFound for empty mark, got %v", err)
	}
}

// Дамп сокращён с живого роутера (2026-08-09). Ключевая деталь: OpkgTun17
// присутствует connected-подсетью 10.66.66.0/24 в НЕСКОЛЬКИХ политиках — NDMS
// раскладывает connected-маршруты всех интерфейсов по каждой таблице политики.
// Отбор обязан смотреть только на дефолт, иначе заберёт лишние политики.
const policyDumpWithOpkgTun = `{
  "Policy0": {"description":"IoT_VPN","mark":"ffffaaa","table4":4096,
    "route4":{"route":[
      {"destination":"0.0.0.0/0","interface":"Wireguard1"},
      {"destination":"10.66.66.0/24","interface":"OpkgTun17"}]}},
  "Policy1": {"description":"North_Korea","mark":"ffffaab","table4":4098,
    "route4":{"route":[
      {"destination":"0.0.0.0/0","interface":"OpkgTun17"},
      {"destination":"10.66.66.0/24","interface":"OpkgTun17"}]}},
  "Policy2": {"description":"SingBox","mark":"ffffaac","table4":4100,
    "route4":{"route":[
      {"destination":"0.0.0.0/0","interface":"OpkgTun17"}]}},
  "Policy3": {"description":"NoRoutes","mark":"ffffaad","table4":4102,
    "route4":{}}
}`

func TestPolicyMarkStore_ListByDefaultInterface(t *testing.T) {
	fg := NewFakeGetter()
	fg.SetRaw("/show/ip/policy", []byte(policyDumpWithOpkgTun))
	s := NewPolicyMarkStore(fg, NopLogger())

	got, err := s.ListByDefaultInterface(context.Background(), "OpkgTun17")
	if err != nil {
		t.Fatalf("ListByDefaultInterface: %v", err)
	}
	want := []PolicyDefaultExit{
		{Name: "Policy1", Mark: "0xffffaab"},
		{Name: "Policy2", Mark: "0xffffaac"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d policies %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestPolicyMarkStore_ListByDefaultInterface_NoMatch(t *testing.T) {
	fg := NewFakeGetter()
	fg.SetRaw("/show/ip/policy", []byte(policyDumpWithOpkgTun))
	s := NewPolicyMarkStore(fg, NopLogger())

	got, err := s.ListByDefaultInterface(context.Background(), "OpkgTun3")
	if err != nil {
		t.Fatalf("ListByDefaultInterface: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %+v", got)
	}
}

func TestPolicyMarkStore_RCIError(t *testing.T) {
	want := errors.New("transport boom")
	fg := NewFakeGetter()
	fg.SetError("/show/ip/policy", want)
	s := NewPolicyMarkStore(fg, NopLogger())

	_, err := s.Get(context.Background(), "Policy0")
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped transport error, got %v", err)
	}
}

// Марка из RCI попадает в текст netfilter.d-хука, который ndm исполняет от
// root, поэтому всё, что не голый hex, обязано отсеиваться на входе.
const policyDumpWithBadMark = `{
  "PolicyBad": {"description":"Broken","mark":"ffff aab","table4":4098,
    "route4":{"route":[{"destination":"0.0.0.0/0","interface":"OpkgTun17"}]}},
  "PolicyInject": {"description":"Injected","mark":"ffff;rm -rf /","table4":4100,
    "route4":{"route":[{"destination":"0.0.0.0/0","interface":"OpkgTun17"}]}},
  "PolicyPrefixed": {"description":"AlreadyHex","mark":"0xffffaae","table4":4102,
    "route4":{"route":[{"destination":"0.0.0.0/0","interface":"OpkgTun17"}]}},
  "PolicyGood": {"description":"Valid","mark":"ffffaac","table4":4104,
    "route4":{"route":[{"destination":"0.0.0.0/0","interface":"OpkgTun17"}]}}
}`

func TestPolicyMarkStore_ListByDefaultInterface_SkipsInvalidMarks(t *testing.T) {
	fg := NewFakeGetter()
	fg.SetRaw("/show/ip/policy", []byte(policyDumpWithBadMark))
	s := NewPolicyMarkStore(fg, NopLogger())

	got, err := s.ListByDefaultInterface(context.Background(), "OpkgTun17")
	if err != nil {
		t.Fatalf("ListByDefaultInterface: %v", err)
	}
	want := []PolicyDefaultExit{{Name: "PolicyGood", Mark: "0xffffaac"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if got[0] != want[0] {
		t.Errorf("got %+v, want %+v", got[0], want[0])
	}

	// Продовая обвязка передаёт nil-логгер — пропуск невалидной марки не
	// должен на нём паниковать.
	if _, err := NewPolicyMarkStore(fg, nil).ListByDefaultInterface(context.Background(), "OpkgTun17"); err != nil {
		t.Fatalf("nil-логгер: %v", err)
	}
}
