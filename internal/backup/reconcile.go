package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/freeturn"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

type freeturnConfigFile struct {
	Clients []freeturn.ClientInstance `json:"clients"`
}

type wdttConfigFile struct {
	Clients []wdtt.ClientInstance `json:"clients"`
}

// ReconcileLinkedEndpoints syncs AWG tunnel Peer.Endpoint to the listen port
// of linked FreeTurn/WDTT clients. Client listen is authoritative — fixes
// archives where listen-repair shuffled proxy ports but left tunnel endpoints stale.
// awgStore — уже сконфигурированное хранилище демона (свой lock-dir), а не
// новый экземпляр: иначе запись шла бы мимо общей блокировки.
func ReconcileLinkedEndpoints(dataDir string, awgStore *storage.AWGTunnelStore) (int, error) {
	dataDir = filepath.Clean(strings.TrimSpace(dataDir))
	if dataDir == "" {
		return 0, fmt.Errorf("data-dir не задан")
	}
	if awgStore == nil {
		return 0, fmt.Errorf("хранилище туннелей не задано")
	}

	ftClients, err := loadFreeTurnClients(filepath.Join(dataDir, "freeturn.json"))
	if err != nil {
		return 0, err
	}
	wdClients, err := loadWdttClients(filepath.Join(dataDir, "wdtt.json"))
	if err != nil {
		return 0, err
	}
	if len(ftClients) == 0 && len(wdClients) == 0 {
		return 0, nil
	}

	tunnels, err := awgStore.List()
	if err != nil {
		return 0, err
	}

	updated := 0
	for i := range tunnels {
		tun := &tunnels[i]
		var listen string
		switch {
		case strings.TrimSpace(tun.FreeTurnClientID) != "":
			listen = ftClients[strings.TrimSpace(tun.FreeTurnClientID)]
		case strings.TrimSpace(tun.WdttClientID) != "":
			listen = wdClients[strings.TrimSpace(tun.WdttClientID)]
		default:
			continue
		}
		want, ok := localEndpoint(listen)
		if !ok {
			continue
		}
		if strings.TrimSpace(tun.Peer.Endpoint) == want {
			continue
		}
		tun.Peer.Endpoint = want
		if err := awgStore.Save(tun); err != nil {
			return updated, fmt.Errorf("tunnel %s: %w", tun.ID, err)
		}
		updated++
	}
	return updated, nil
}

func loadFreeTurnClients(path string) (map[string]string, error) {
	out := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	var cfg freeturnConfigFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("freeturn.json: %w", err)
	}
	for _, c := range cfg.Clients {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			continue
		}
		out[id] = strings.TrimSpace(c.Config.Listen)
	}
	return out, nil
}

func loadWdttClients(path string) (map[string]string, error) {
	out := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	var cfg wdttConfigFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("wdtt.json: %w", err)
	}
	for _, c := range cfg.Clients {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			continue
		}
		out[id] = strings.TrimSpace(c.Config.Listen)
	}
	return out, nil
}

func localEndpoint(listen string) (string, bool) {
	port, ok := freeturn.LocalListenPort(listen)
	if !ok || port <= 0 {
		if p := wdtt.ListenPortFromAddr(listen); p > 0 {
			port = p
		} else {
			return "", false
		}
	}
	return fmt.Sprintf("127.0.0.1:%d", port), true
}
