package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/opkg"
)

// newOpkgHandler подсовывает вместо opkg скрипт с заданным поведением.
func newOpkgHandler(t *testing.T, script string) *SystemToolsHandler {
	t.Helper()
	h, _ := newSystemToolsForTest(t, "expert")
	bin := filepath.Join(t.TempDir(), "opkg")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	h.opkg = opkg.NewClient()
	h.opkg.Bin = bin
	return h
}

// Роутер, где обновлять нечего, отдаёт пустой вывод и exit 0. Пустой список
// обязан приехать массивом: `data: null` роняет рендер вкладки «Обновления».
func TestOpkgEmptyListsAreArrays(t *testing.T) {
	h := newOpkgHandler(t, "#!/bin/sh\nexit 0\n")

	cases := []struct {
		name string
		url  string
		call func(*httptest.ResponseRecorder, string)
		want string
	}{
		{"upgradable", "/api/system/opkg/upgradable", func(w *httptest.ResponseRecorder, u string) {
			h.OpkgUpgradable(w, httptest.NewRequest("GET", u, nil))
		}, `{"success":true,"data":[]}`},
		{"installed", "/api/system/opkg/installed", func(w *httptest.ResponseRecorder, u string) {
			h.OpkgInstalled(w, httptest.NewRequest("GET", u, nil))
		}, `{"success":true,"data":[]}`},
		{"search", "/api/system/opkg/search?q=curl", func(w *httptest.ResponseRecorder, u string) {
			h.OpkgSearch(w, httptest.NewRequest("GET", u, nil))
		}, `{"success":true,"data":[]}`},
		{"available", "/api/system/opkg/available?limit=50", func(w *httptest.ResponseRecorder, u string) {
			h.OpkgAvailable(w, httptest.NewRequest("GET", u, nil))
		}, `{"success":true,"data":{"items":[],"total":0,"offset":0,"limit":50}}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			c.call(rr, c.url)
			got := strings.TrimSpace(rr.Body.String())
			if got != c.want {
				t.Errorf("тело ответа = %s, ожидалось %s", got, c.want)
			}
		})
	}
}
