package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/lock"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

// AWGTunnelStore provides directory-based storage for AmneziaWG tunnel metadata.
type AWGTunnelStore struct {
	dir      string
	lockName string
	lockDir  string
	timeout  time.Duration
}

// NewAWGTunnelStore creates a new AWG tunnel store.
func NewAWGTunnelStore(dir string) *AWGTunnelStore {
	return NewAWGTunnelStoreWithLockDir(dir, lock.LockDir)
}

// NewAWGTunnelStoreWithLockDir creates a new AWG tunnel store with custom lock directory.
func NewAWGTunnelStoreWithLockDir(dir string, lockDir string) *AWGTunnelStore {
	return &AWGTunnelStore{
		dir:      dir,
		lockName: "tunnels",
		lockDir:  lockDir,
		timeout:  5 * time.Second,
	}
}

// List returns all AWG tunnels by scanning the directory.
func (s *AWGTunnelStore) List() ([]AWGTunnel, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []AWGTunnel{}, nil
		}
		return nil, fmt.Errorf("read tunnels directory: %w", err)
	}

	var tunnels []AWGTunnel
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(s.dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var tunnel AWGTunnel
		if err := json.Unmarshal(data, &tunnel); err != nil {
			// Quarantine instead of skipping silently: a skipped file's ID
			// looks free to NextAvailableID, and the next created tunnel
			// would overwrite the still-recoverable JSON (with its keys).
			QuarantineCorrupt(path, err)
			continue
		}

		if tunnel.Type == "" {
			tunnel.Type = "awg"
		}

		// Migration: old tunnels without DefaultRouteSet default to DefaultRoute=true
		if !tunnel.DefaultRouteSet {
			tunnel.DefaultRoute = true
			tunnel.DefaultRouteSet = true
		}

		tunnels = append(tunnels, tunnel)
	}

	return tunnels, nil
}

// ListStrict — перечисление БЕЗ прощения и БЕЗ побочных действий: любая
// пофайловая беда (чтение, JSON) — ошибка всего вызова; карантина нет.
//
// Нужен там, где «не смогли перечислить» и «записей нет» имеют
// противоположные последствия, а List() их не различает — пофайловую ошибку
// он глотает через continue и уносит файл в карантин побочным
// переименованием. Два таких потребителя: занятость номеров OpkgTun (битая
// запись иначе молча освобождает номер живого туннеля) и гейт посева реестра
// выходов, где временно нечитаемый каталог выглядел бы как «терять нечего».
// Отсутствие каталога — законное «пусто»: записей не создавали.
//
// МИГРАЦИЙ ЗДЕСЬ НЕТ — в отличие от List() (DefaultRoute и всё, что добавят
// после). Потребители читают только Backend, ID и Interface.Address, которых
// миграции не касаются; добавляя миграцию в List(), решить осознанно, нужна
// ли она и тут — молча разойтись эти два перечисления могут легко.
func (s *AWGTunnelStore) ListStrict() ([]AWGTunnel, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []AWGTunnel{}, nil
		}
		return nil, fmt.Errorf("read tunnels directory: %w", err)
	}
	var tunnels []AWGTunnel
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		var tunnel AWGTunnel
		if err := json.Unmarshal(data, &tunnel); err != nil {
			return nil, fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		if tunnel.Type == "" {
			tunnel.Type = "awg"
		}
		tunnels = append(tunnels, tunnel)
	}
	return tunnels, nil
}

// Get returns a single tunnel by ID.
func (s *AWGTunnelStore) Get(id string) (*AWGTunnel, error) {
	path := filepath.Join(s.dir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("tunnel not found: %s", id)
		}
		return nil, fmt.Errorf("read tunnel file: %w", err)
	}

	var tunnel AWGTunnel
	if err := json.Unmarshal(data, &tunnel); err != nil {
		return nil, fmt.Errorf("parse tunnel JSON: %w", err)
	}

	if tunnel.Type == "" {
		tunnel.Type = "awg"
	}

	// Migration: old tunnels without DefaultRouteSet default to DefaultRoute=true
	if !tunnel.DefaultRouteSet {
		tunnel.DefaultRoute = true
		tunnel.DefaultRouteSet = true
	}

	return &tunnel, nil
}

// Save writes tunnel to disk.
func (s *AWGTunnelStore) Save(tunnel *AWGTunnel) error {
	lk, err := lock.WaitLockDir(s.lockName, s.lockDir, s.timeout)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lk.Unlock()

	if tunnel.Type == "" {
		tunnel.Type = "awg"
	}

	// Use Encoder with SetEscapeHTML(false) to preserve < and > in signature fields (I1-I5)
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(tunnel); err != nil {
		return fmt.Errorf("marshal tunnel: %w", err)
	}

	// Remove trailing newline added by Encode
	data := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))

	path := filepath.Join(s.dir, tunnel.ID+".json")
	if err := AtomicWrite(path, data); err != nil {
		return fmt.Errorf("write tunnel file: %w", err)
	}

	return nil
}

// Delete removes tunnel file.
func (s *AWGTunnelStore) Delete(id string) error {
	lk, err := lock.WaitLockDir(s.lockName, s.lockDir, s.timeout)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lk.Unlock()

	path := filepath.Join(s.dir, id+".json")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("tunnel not found: %s", id)
		}
		return fmt.Errorf("check tunnel file: %w", err)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove tunnel file: %w", err)
	}

	return nil
}

// Exists checks if tunnel exists.
func (s *AWGTunnelStore) Exists(id string) bool {
	path := filepath.Join(s.dir, id+".json")
	_, err := os.Stat(path)
	return err == nil
}

// ClearRuntimeState clears volatile runtime fields (ActiveWAN, StartedAt)
// for a tunnel. Called after Stop/Suspend when the tunnel is no longer active.
func (s *AWGTunnelStore) ClearRuntimeState(id string) {
	stored, err := s.Get(id)
	if err != nil {
		return
	}
	changed := false
	if stored.ActiveWAN != "" {
		stored.ActiveWAN = ""
		changed = true
	}
	if stored.StartedAt != "" {
		stored.StartedAt = ""
		changed = true
	}
	if changed {
		_ = s.Save(stored)
	}
}

const (
	// OS 5.x: карта числовых индексов туннелей:
	//   OpkgTun0..9   — зарезервированы под fakeip-движок sing-box
	//                   (см. internal/singbox/router, tunNDMSName);
	//   OpkgTun10..16 — kernel-AWG туннели (awg10..awg16); потолок 16 —
	//                   прошивочный лимит NDMS на индекс OpkgTun;
	//   awg20+        — NativeWG: чистые storage-ключи, в OpkgTun НЕ
	//                   отображаются (NDMS-интерфейс — WireguardN по
	//                   NWGIndex, kernel-интерфейс — nwgN).
	os5MinIndex = 10
	os5MaxIndex = 16
	// os5NWGMinIndex — начало диапазона ID для NativeWG на OS 5.x. Сверху
	// диапазон не ограничен: реальную ёмкость задают индексы Wireguard0..99
	// в NDMS (nwg.MaxTunnels) и слоты awg_proxy (16 одновременных туннелей
	// с обфускацией на прошивках без нативного ASC), а не диапазон ID.
	// Легаси NativeWG-туннели, созданные до разделения диапазонов, могут
	// занимать awg10..awg16 — это допустимо, kernel-аллокатор просто
	// пропускает занятые ими номера (миграция не выполняется).
	os5NWGMinIndex = 20
)

// NextAvailableID finds the next available tunnel ID for the given backend
// ("kernel" | "nativewg"; любое другое значение, включая пустое, трактуется
// как kernel).
// - OS 5.x, kernel:   awg10..awg16 → OpkgTun10..OpkgTun16 (прошивочный лимит NDMS — 16)
// - OS 5.x, nativewg: awg20, awg21, ... (в OpkgTun не отображаются, см. os5NWGMinIndex)
// - OS 4.x: awgm0, awgm1, ... (uses 'm' prefix, no NDMS; backend не различается)
func (s *AWGTunnelStore) NextAvailableID(backend string) (string, error) {
	tunnels, err := s.List()
	if err != nil {
		return "", err
	}
	return nextAvailableID(tunnels, backend, osdetect.Is5())
}

// nextAvailableID — чистая функция выбора ID (вынесена из NextAvailableID
// для тестируемости без глобального osdetect-состояния).
func nextAvailableID(tunnels []AWGTunnel, backend string, is5 bool) (string, error) {
	existing := make(map[int]bool)

	if is5 {
		// Занятые номера собираются по ВСЕМ туннелям независимо от backend —
		// так диапазоны не коллидируют между собой (легаси NativeWG на awg12
		// продолжает занимать номер в kernel-диапазоне, и наоборот).
		for _, t := range tunnels {
			if len(t.ID) > 3 && t.ID[:3] == "awg" {
				if num, err := strconv.Atoi(t.ID[3:]); err == nil {
					existing[num] = true
				}
			}
		}
		if backend == "nativewg" {
			for i := os5NWGMinIndex; ; i++ {
				if !existing[i] {
					return "awg" + strconv.Itoa(i), nil
				}
			}
		}
		for i := os5MinIndex; i <= os5MaxIndex; i++ {
			if !existing[i] {
				return "awg" + strconv.Itoa(i), nil
			}
		}
		return "", fmt.Errorf("maximum number of tunnels reached (%d)", os5MaxIndex-os5MinIndex+1)
	} else {
		for _, t := range tunnels {
			if len(t.ID) > 4 && t.ID[:4] == "awgm" {
				if num, err := strconv.Atoi(t.ID[4:]); err == nil {
					existing[num] = true
				}
			}
		}
		for i := 0; ; i++ {
			if !existing[i] {
				return "awgm" + strconv.Itoa(i), nil
			}
		}
	}
}
