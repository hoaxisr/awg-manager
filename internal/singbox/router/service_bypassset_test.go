package router

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

type stubCounts map[string]int

func (s stubCounts) GeoIPTagCounts() map[string]int { return s }

func TestValidateBypassGeoIPTags(t *testing.T) {
	svc := &ServiceImpl{deps: Deps{GeoTagCounts: stubCounts{"ru": 20000, "big": 500000}}}
	cases := []struct {
		name string
		tags []string
		ok   bool
	}{
		{"empty_ok", nil, true},
		{"budget_ok", []string{"ru"}, true},
		{"budget_exceeded", []string{"big"}, false},
		{"unknown_tag_ok_zero_count", []string{"nosuch"}, true},
		{"case_insensitive", []string{"RU"}, true},
		{"sum_over_budget", []string{"ru", "big"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sr := storage.SingboxRouterSettings{BypassGeoIPTags: c.tags}
			err := svc.validateBypassGeoIPTags(sr)
			if c.ok && err != nil {
				t.Fatalf("want ok, got error: %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

// Без счётчика (deps.GeoTagCounts == nil) валидация не должна падать: бюджет
// просто неизвестен, а UpdateSettings обязан оставаться рабочим.
func TestValidateBypassGeoIPTagsNilCounter(t *testing.T) {
	svc := &ServiceImpl{}
	sr := storage.SingboxRouterSettings{BypassGeoIPTags: []string{"big"}}
	if err := svc.validateBypassGeoIPTags(sr); err != nil {
		t.Fatalf("want nil error without counter, got %v", err)
	}
}
