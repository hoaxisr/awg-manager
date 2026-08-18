package api

import (
	"context"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/freeturn"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

type stubFreeTurnForImport struct {
	cfg freeturn.Config
}

func (s *stubFreeTurnForImport) GetConfig() (freeturn.Config, error) { return s.cfg, nil }

func (s *stubFreeTurnForImport) UpdateClientConfig(freeturn.ClientConfig) error { return nil }
func (s *stubFreeTurnForImport) UpdateServerConfig(freeturn.ServerConfig) error { return nil }
func (s *stubFreeTurnForImport) UpdateClientInstance(string, freeturn.ClientConfig) error {
	return nil
}
func (s *stubFreeTurnForImport) UpdateServerInstance(string, freeturn.ServerConfig) error { return nil }
func (s *stubFreeTurnForImport) CreateClient(freeturn.CreateClientInput) (freeturn.ClientInstance, error) {
	return freeturn.ClientInstance{}, nil
}
func (s *stubFreeTurnForImport) CreateServer(freeturn.CreateServerInput) (freeturn.ServerInstance, error) {
	return freeturn.ServerInstance{}, nil
}
func (s *stubFreeTurnForImport) DeleteClient(string) error         { return nil }
func (s *stubFreeTurnForImport) DeleteServer(string) error         { return nil }
func (s *stubFreeTurnForImport) RenameClient(string, string) error { return nil }
func (s *stubFreeTurnForImport) RenameServer(string, string) error { return nil }
func (s *stubFreeTurnForImport) ServerConfigForLink(string) (freeturn.ServerConfig, error) {
	return freeturn.ServerConfig{}, nil
}
func (s *stubFreeTurnForImport) Status() freeturn.Status               { return freeturn.Status{} }
func (s *stubFreeTurnForImport) StartClient() error                    { return nil }
func (s *stubFreeTurnForImport) StopClient() error                     { return nil }
func (s *stubFreeTurnForImport) StartServer() error                    { return nil }
func (s *stubFreeTurnForImport) StopServer() error                     { return nil }
func (s *stubFreeTurnForImport) StartClientInstance(string) error      { return nil }
func (s *stubFreeTurnForImport) StopClientInstance(string) error       { return nil }
func (s *stubFreeTurnForImport) StartServerInstance(string) error      { return nil }
func (s *stubFreeTurnForImport) StopServerInstance(string) error       { return nil }
func (s *stubFreeTurnForImport) InstallBinaries(context.Context) error { return nil }
func (s *stubFreeTurnForImport) Stop()                                 {}
func (s *stubFreeTurnForImport) ListServerAllowlist(string) (freeturn.AllowlistStatus, error) {
	return freeturn.AllowlistStatus{}, nil
}
func (s *stubFreeTurnForImport) AddServerAllowlistClient(string, string, string) (freeturn.AddAllowlistResult, error) {
	return freeturn.AddAllowlistResult{}, nil
}
func (s *stubFreeTurnForImport) RemoveServerAllowlistClient(string, string) error { return nil }
func (s *stubFreeTurnForImport) DisableServerAllowlist(string) (bool, error)      { return false, nil }
func (s *stubFreeTurnForImport) CaptchaStatus() freeturn.CaptchaOverview {
	return freeturn.CaptchaOverview{}
}
func (s *stubFreeTurnForImport) CaptchaStatusForClient(string) (freeturn.CaptchaClientStatus, bool) {
	return freeturn.CaptchaClientStatus{}, false
}

type stubWdttForImport struct {
	cfg wdtt.Config
}

func (s *stubWdttForImport) GetConfig() (wdtt.Config, error) { return s.cfg, nil }

func (s *stubWdttForImport) UpdateClientConfig(wdtt.ClientConfig) error           { return nil }
func (s *stubWdttForImport) UpdateClientInstance(string, wdtt.ClientConfig) error { return nil }
func (s *stubWdttForImport) CreateClient(wdtt.CreateClientInput) (wdtt.ClientInstance, error) {
	return wdtt.ClientInstance{}, nil
}
func (s *stubWdttForImport) DeleteClient(string) error         { return nil }
func (s *stubWdttForImport) RenameClient(string, string) error { return nil }
func (s *stubWdttForImport) ImportLink(string, string) (wdtt.ClientInstance, wdtt.ImportPayload, error) {
	return wdtt.ClientInstance{}, wdtt.ImportPayload{}, nil
}
func (s *stubWdttForImport) DecodeLink(string) (wdtt.LinkDecodeResult, error) {
	return wdtt.LinkDecodeResult{}, nil
}
func (s *stubWdttForImport) Status() wdtt.Status              { return wdtt.Status{} }
func (s *stubWdttForImport) StartClient() error               { return nil }
func (s *stubWdttForImport) StopClient() error                { return nil }
func (s *stubWdttForImport) StartClientInstance(string) error { return nil }
func (s *stubWdttForImport) StopClientInstance(string) error  { return nil }
func (s *stubWdttForImport) RefreshSubscription(string) (wdtt.ClientInstance, wdtt.ImportPayload, error) {
	return wdtt.ClientInstance{}, wdtt.ImportPayload{}, nil
}
func (s *stubWdttForImport) UpdateServerConfig(wdtt.ServerConfig) error { return nil }
func (s *stubWdttForImport) UpdateServerInstance(string, wdtt.ServerConfig) (wdtt.ServerConfig, error) {
	return wdtt.ServerConfig{}, nil
}
func (s *stubWdttForImport) SetServerNATMode(context.Context, string, string) (wdtt.ServerConfig, error) {
	return wdtt.ServerConfig{}, nil
}
func (s *stubWdttForImport) SetServerPolicy(context.Context, string, string) (wdtt.ServerConfig, error) {
	return wdtt.ServerConfig{}, nil
}
func (s *stubWdttForImport) SetServerLANSegments(context.Context, string, []string) (wdtt.ServerConfig, error) {
	return wdtt.ServerConfig{}, nil
}
func (s *stubWdttForImport) CreateServer(wdtt.CreateServerInput) (wdtt.ServerInstance, error) {
	return wdtt.ServerInstance{}, nil
}
func (s *stubWdttForImport) DeleteServer(string) error         { return nil }
func (s *stubWdttForImport) RenameServer(string, string) error { return nil }
func (s *stubWdttForImport) ServerConfigForLink(string) (wdtt.ServerConfig, error) {
	return wdtt.ServerConfig{}, nil
}
func (s *stubWdttForImport) StartServer() error                    { return nil }
func (s *stubWdttForImport) StopServer() error                     { return nil }
func (s *stubWdttForImport) StartServerInstance(string) error      { return nil }
func (s *stubWdttForImport) StopServerInstance(string) error       { return nil }
func (s *stubWdttForImport) InstallBinaries(context.Context) error { return nil }
func (s *stubWdttForImport) Stop()                                 {}
func (s *stubWdttForImport) ListServerClients(string) (wdtt.ServerClientsStatus, error) {
	return wdtt.ServerClientsStatus{}, nil
}
func (s *stubWdttForImport) AddServerClient(string, string, string, string, string) (wdtt.ServerClientsStatus, error) {
	return wdtt.ServerClientsStatus{}, nil
}
func (s *stubWdttForImport) RenameServerClient(string, string, string) (wdtt.ServerClientsStatus, error) {
	return wdtt.ServerClientsStatus{}, nil
}
func (s *stubWdttForImport) RemoveServerClient(string, string) (wdtt.ServerClientsStatus, error) {
	return wdtt.ServerClientsStatus{}, nil
}

func TestImportHandler_patchImportContentForLinkedClient_UsesFreeTurnListen(t *testing.T) {
	h := &ImportHandler{
		freeturn: &stubFreeTurnForImport{
			cfg: freeturn.Config{
				Clients: []freeturn.ClientInstance{{
					ID:     "ft-1",
					Config: freeturn.ClientConfig{Listen: "127.0.0.1:9001"},
				}},
			},
		},
	}
	raw := `[Interface]
PrivateKey = abc
[Peer]
PublicKey = def
Endpoint = 127.0.0.1:9000
`
	got := h.patchImportContentForLinkedClient(raw, "ft-1", "")
	if !strings.Contains(got, "Endpoint = 127.0.0.1:9001") {
		t.Fatalf("expected endpoint 9001 from client listen, got:\n%s", got)
	}
}

func TestImportHandler_patchImportContentForLinkedClient_UsesWdttListen(t *testing.T) {
	h := &ImportHandler{
		wdtt: &stubWdttForImport{
			cfg: wdtt.Config{
				Clients: []wdtt.ClientInstance{{
					ID:     "wd-1",
					Config: wdtt.ClientConfig{Listen: "127.0.0.1:9002"},
				}},
			},
		},
	}
	raw := `[Interface]
PrivateKey = abc
[Peer]
PublicKey = def
Endpoint = 127.0.0.1:9000
`
	got := h.patchImportContentForLinkedClient(raw, "", "wd-1")
	if !strings.Contains(got, "Endpoint = 127.0.0.1:9002") {
		t.Fatalf("expected endpoint 9002 from wdtt listen, got:\n%s", got)
	}
}
