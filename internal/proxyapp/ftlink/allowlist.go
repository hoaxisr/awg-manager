package ftlink

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
)

// AllowlistEntry is one authorized freeturn client ID with optional comment.
type AllowlistEntry struct {
	ClientID string `json:"clientId"`
	Comment  string `json:"comment,omitempty"`
}

// AllowlistStatus is returned by the allowlist API.
type AllowlistStatus struct {
	Enabled     bool             `json:"enabled"`
	ClientsFile string           `json:"clientsFile,omitempty"`
	Clients     []AllowlistEntry `json:"clients"`
}

// AddAllowlistResult is returned after adding a client to the allowlist.
type AddAllowlistResult struct {
	AllowlistStatus
	NeedsRestart bool `json:"needsRestart"`
}

type allowlistFile struct {
	Clients map[string]struct {
		Comment string `json:"comment,omitempty"`
	} `json:"clients"`
}

var allowlistClientIDRe = regexp.MustCompile(`^[0-9a-fA-F]{16,64}$`)

// defaultAllowlistPath — путь списка по умолчанию. Формат имени живёт в
// instancestore: тот же путь читает уборка при удалении инстанса, и две копии
// формулы разъехались бы, оставив файл сиротой.
func defaultAllowlistPath(dataDir, serverID string) string {
	return instancestore.FreeTurnAllowlistPath(dataDir, serverID)
}

func validateAllowlistClientID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("client ID не задан")
	}
	if len(id) > 255 {
		return fmt.Errorf("client ID слишком длинный (макс. 255 символов)")
	}
	if !allowlistClientIDRe.MatchString(id) {
		return fmt.Errorf("client ID должен быть hex-строкой (16–64 символа)")
	}
	return nil
}

func readAllowlistFile(path string) (allowlistFile, error) {
	var data allowlistFile
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			data.Clients = make(map[string]struct {
				Comment string `json:"comment,omitempty"`
			})
			return data, nil
		}
		return data, err
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return data, fmt.Errorf("разбор allowlist %s: %w", path, err)
	}
	if data.Clients == nil {
		data.Clients = make(map[string]struct {
			Comment string `json:"comment,omitempty"`
		})
	}
	return data, nil
}

func writeAllowlistFile(path string, data allowlistFile) error {
	if data.Clients == nil {
		data.Clients = make(map[string]struct {
			Comment string `json:"comment,omitempty"`
		})
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func allowlistEntriesFromFile(data allowlistFile) []AllowlistEntry {
	out := make([]AllowlistEntry, 0, len(data.Clients))
	for id, info := range data.Clients {
		out = append(out, AllowlistEntry{ClientID: id, Comment: info.Comment})
	}
	// Stable order: by comment then id.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			a, b := out[i], out[j]
			if strings.ToLower(a.Comment) > strings.ToLower(b.Comment) ||
				(a.Comment == b.Comment && a.ClientID > b.ClientID) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func loadAllowlistStatus(clientsFile string) (AllowlistStatus, error) {
	st := AllowlistStatus{Enabled: strings.TrimSpace(clientsFile) != "", ClientsFile: clientsFile}
	if !st.Enabled {
		st.Clients = []AllowlistEntry{}
		return st, nil
	}
	data, err := readAllowlistFile(clientsFile)
	if err != nil {
		return st, err
	}
	st.Clients = allowlistEntriesFromFile(data)
	return st, nil
}

func addAllowlistClient(path, clientID, comment string) error {
	if err := validateAllowlistClientID(clientID); err != nil {
		return err
	}
	data, err := readAllowlistFile(path)
	if err != nil {
		return err
	}
	data.Clients[strings.ToLower(clientID)] = struct {
		Comment string `json:"comment,omitempty"`
	}{Comment: strings.TrimSpace(comment)}
	return writeAllowlistFile(path, data)
}

func removeAllowlistClient(path, clientID string) error {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return fmt.Errorf("client ID не задан")
	}
	data, err := readAllowlistFile(path)
	if err != nil {
		return err
	}
	delete(data.Clients, strings.ToLower(clientID))
	return writeAllowlistFile(path, data)
}
