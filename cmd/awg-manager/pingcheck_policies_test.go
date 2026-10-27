package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// policyGetter отдаёт /show/rc/ip/policy из подменяемой строки.
type policyGetter struct{ body *string }

func (g policyGetter) GetRaw(context.Context, string) ([]byte, error) { return []byte(*g.body), nil }
func (g policyGetter) Get(_ context.Context, _ string, dst any) error {
	return json.Unmarshal([]byte(*g.body), dst)
}
func (g policyGetter) Post(context.Context, any) (json.RawMessage, error) {
	return nil, errors.New("not used")
}

// Мониторинг решает по свежему списку политик: кэш живёт час, а HR Neo и
// веб-интерфейс роутера правят политики мимо демона (F743, стенд 27.10:
// снятие молча не нашло туннель в политике, созданной после старта).
func TestPingcheckPolicies_FreshPoliciesBypassesCache(t *testing.T) {
	body := `{"Policy0":{"permit":[{"enabled":true,"interface":"PPPoE0"}]}}`
	store := ndmsquery.NewPolicyStore(policyGetter{&body}, nil)
	if _, err := store.List(context.Background()); err != nil { // кэш прогрет
		t.Fatal(err)
	}
	body = `{"Policy0":{"permit":[{"enabled":true,"interface":"PPPoE0"}]},"HydraRoute":{"permit":[{"enabled":true,"interface":"OpkgTun7"}]}}`

	got, err := pingcheckPolicies{list: store}.FreshPolicies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("политик %d, want 2: список взят из кэша", len(got))
	}
}
