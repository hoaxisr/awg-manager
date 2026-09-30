package aiassistant

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfigStoreKeepsAPIKeyWriteOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-assistant.json")
	store, err := NewConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := store.Save(ConfigUpdate{Enabled: true, Provider: "openai", Model: "test-model", APIKey: "secret-key"})
	if err != nil {
		t.Fatal(err)
	}
	if !pub.APIKeySet || store.Get().APIKey != "secret-key" {
		t.Fatalf("key state not preserved: %+v", pub)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}

	// Empty key means preserve the existing secret when changing the model.
	if _, err := store.Save(ConfigUpdate{Enabled: true, Provider: "openai", Model: "new-model"}); err != nil {
		t.Fatal(err)
	}
	if store.Get().APIKey != "secret-key" {
		t.Fatal("empty update erased API key")
	}
}

func TestConfigStoreRequiresCredentialsWhenEnabled(t *testing.T) {
	store, err := NewConfigStore(filepath.Join(t.TempDir(), "ai-assistant.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ConfigUpdate{Enabled: true, Provider: "openai"}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestConfigStoreDoesNotReuseKeyAcrossProviders(t *testing.T) {
	store, err := NewConfigStore(filepath.Join(t.TempDir(), "ai-assistant.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ConfigUpdate{Enabled: true, Provider: "openai", Model: "gpt", APIKey: "openai-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ConfigUpdate{
		Enabled: true, Provider: "local_embedded", Model: "qwen",
		BaseURL: DefaultEmbeddedBaseURL,
	}); err != nil {
		t.Fatal(err)
	}
	if store.Get().APIKey != "" {
		t.Fatal("provider switch retained the previous provider API key")
	}
}

func TestConfigStoreRejectsUnsafeProviderURLAndResourceLimits(t *testing.T) {
	store, err := NewConfigStore(filepath.Join(t.TempDir(), "ai-assistant.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ConfigUpdate{
		Enabled: true, Provider: "openai", Model: "gpt", APIKey: "secret",
		BaseURL: "http://127.0.0.1:9999/v1",
	}); err == nil {
		t.Fatal("expected official provider URL validation error")
	}
	if _, err := store.Save(ConfigUpdate{
		Provider: "local_embedded", Model: "qwen",
		LocalEngine: &LocalEngineConfig{Port: 11435, ContextSize: 16384, Threads: 8},
	}); err == nil {
		t.Fatal("expected local resource limit validation error")
	}
}

func TestConfigStoreAcceptsGoogleAndRequiresItsOwnKey(t *testing.T) {
	store, err := NewConfigStore(filepath.Join(t.TempDir(), "ai-assistant.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ConfigUpdate{
		Enabled: true, Provider: "google", Model: "gemini-3.7-flash",
	}); err == nil {
		t.Fatal("expected Google API key validation error")
	}
	pub, err := store.Save(ConfigUpdate{
		Enabled: true, Provider: "google", Model: "gemini-3.7-flash", APIKey: "google-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !pub.APIKeySet || pub.Provider != "google" {
		t.Fatalf("public config = %+v", pub)
	}
	if _, err := store.Save(ConfigUpdate{
		Enabled: true, Provider: "google", Model: "gemini-3.7-flash", APIKey: "google-secret",
		BaseURL: "https://example.com/v1beta",
	}); err == nil {
		t.Fatal("expected Google host validation error")
	}
}

func TestConfigStorePersistsModelRoute(t *testing.T) {
	store, err := NewConfigStore(filepath.Join(t.TempDir(), "ai-assistant.json"))
	if err != nil {
		t.Fatal(err)
	}
	pub, err := store.Save(ConfigUpdate{
		Enabled: true, Provider: "google", Model: "gemini-3.6-flash", APIKey: "secret",
		RouteTag: "sub-test", RouteKind: "subscription",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pub.RouteTag != "sub-test" || pub.RouteKind != "subscription" {
		t.Fatalf("public route = %q/%q", pub.RouteKind, pub.RouteTag)
	}
	if got := store.Get(); got.RouteTag != "sub-test" || got.RouteKind != "subscription" {
		t.Fatalf("stored route = %q/%q", got.RouteKind, got.RouteTag)
	}
}
