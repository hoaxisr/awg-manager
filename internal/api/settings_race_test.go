package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Красный до фикса под -race: GET /settings/get маршалит ЖИВОЙ объект
// (map ServerPeerSecrets) одновременно с локированной записью в ту же map —
// data race; без -race это fatal concurrent map read and map write
// (падение демона).
func TestSettingsGet_NoRaceWithPeerSecretWrites(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_ = store.SetServerPeerSecret("Wireguard0", fmt.Sprintf("pk%d", i), storage.ServerPeerSecret{PrivateKey: "x"})
		}
	}()
	for i := 0; i < 200; i++ {
		req := httptest.NewRequest(http.MethodGet, "/settings/get", nil)
		h.Get(httptest.NewRecorder(), req)
	}
	<-done
}

// Красный до фикса под -race: RegenerateApiKey пишет settings.ApiKey без
// лока (живой указатель из Get) и маршалит живой объект в ответ; GetApiKey
// (auth middleware) читает то же поле.
func TestRegenerateApiKey_NoRaceWithApiKeyReads(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_ = store.GetApiKey()
		}
	}()
	for i := 0; i < 200; i++ {
		req := httptest.NewRequest(http.MethodPost, "/settings/regenerate-api-key", nil)
		h.RegenerateApiKey(httptest.NewRecorder(), req)
	}
	<-done
}
