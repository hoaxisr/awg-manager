package instancestore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// SeedDeps — зависимости посева. LivePermits и AllocIndex замыкает проводка
// (задача 14): первый — наблюдение политик (RCI), второй — proxyrt.Allocator
// с формулой taken, не отбирающей у владельца его живой интерфейс (B2).
type SeedDeps struct {
	WdttPath     string
	FreeturnPath string
	// RuntimeDir — каталог pid-файлов СТАРОГО мира: <dataDir>/run (блокер B3:
	// roles.RuntimeDir = /tmp/awgm — ДРУГОЙ каталог, kill-list был бы пуст).
	RuntimeDir  string
	LivePermits func(ctx context.Context, ndmsIface string) ([]string, error)
	// AllocIndex: havePin=true — просим сохранить конкретный индекс (Щ13:
	// ноль — законный пин на mips, сентинелом не является).
	AllocIndex func(owner string, pinned int, havePin bool) (int, error)
	GOARCH     string
}

type SeedResult struct {
	State     State
	SeededNow bool
	// CleanupPending — одноразовые шаги не доведены: либо посев только что
	// состоялся, либо прошлый проход не удался и отметка на диске висит.
	CleanupPending bool
	// OldGenPIDs — кандидаты на добивание (§9 протокола). Только собраны:
	// живость и принадлежность бинарю проверяет адаптер (задача 6, B3 —
	// pid-файл на флешке переживает ребут, PID мог достаться постороннему).
	OldGenPIDs []int
	// LegacyKernelIfaces — прежние kernel-имена сервера: вход одноразовой
	// уборки непомеченных правил (план 3, residual I-1(а)).
	LegacyKernelIfaces []string
}

// Узкие DTO СТАРЫХ форматов. Не импортируем internal/wdtt|freeturn: пакеты
// умирают в этой же волне, а посев живёт. Незнакомые поля игнорируются — это
// чтение чужого формата, не наш файл.

type oldWdttFile struct {
	Clients []struct {
		ID     string        `json:"id"`
		Name   string        `json:"name"`
		Config oldWdttClient `json:"config"`
	} `json:"clients"`
	Servers []struct {
		ID     string        `json:"id"`
		Name   string        `json:"name"`
		Config oldWdttServer `json:"config"`
	} `json:"servers"`
}

type oldWdttClient struct {
	Enabled     bool   `json:"enabled"`
	Listen      string `json:"listen"`
	Peer        string `json:"peer"`
	Password    string `json:"password"`
	VKHashes    string `json:"vkHashes"`
	Workers     int    `json:"workers"`
	Obfs        string `json:"obfs"`
	Fingerprint string `json:"fingerprint"`
	DeviceID    string `json:"deviceId"`
	CaptchaMode string `json:"captchaMode"`
	VKAuthMode  string `json:"vkAuthMode"`
	Sub         string `json:"sub"`
	ConnMode    string `json:"connMode"`
	PeerWg      string `json:"peerWg"`
	PeerRaw     string `json:"peerRaw"`
	NdmsIface   string `json:"ndmsIface"`
	RawIface    string `json:"rawIface"`
	// RawClientIP/RawClientMTU НЕ читаются: кэш факта — в наблюдение (§9).
	// Debug НЕ читается: мёртвое поле — старый мир -debug клиенту не эмитил
	// (проверено грепом; решение Р10).
	PolicyPermits []oldPolicyPermit `json:"policyPermits"`
}

// oldPolicyPermit — permit старого формата. Order здесь ЗНАЧЕНИЕ, а не
// указатель, ровно как в wdtt/types.go:68-71: тег там без omitempty, поэтому
// `"order": 0` записан на диск явно и означает ВЕРХ политики (NDMS нумерует
// permit'ы с нуля), а не «позиция не задана».
type oldPolicyPermit struct {
	Name  string `json:"name"`
	Order int    `json:"order"`
}

type oldServerClient struct {
	Password  string `json:"password"`
	Comment   string `json:"comment"`
	VkHash    string `json:"vkHash"`
	ExpiresAt int64  `json:"expiresAt"`
	Auto      bool   `json:"auto"`
}

type oldWdttServer struct {
	Enabled      bool     `json:"enabled"`
	Listen       string   `json:"listen"`
	WgPort       int      `json:"wgPort"`
	ConfigDir    string   `json:"configDir"`
	Password     string   `json:"password"`
	AdminID      string   `json:"adminId"`
	BotToken     string   `json:"botToken"`
	NatIface     string   `json:"natIface"`
	NatMode      string   `json:"natMode"`
	NatStaticWAN string   `json:"natStaticWan"`
	Policy       string   `json:"policy"`
	LanSegments  []string `json:"lanSegments"`
	// OpenFirewall — *bool с семантикой «nil = true» (wdtt/types.go:118-120).
	// Читается указателем именно ради этой семантики: в новом конфиге поле
	// обычный bool, и отсутствующий ключ обязан стать true, иначе у всех, кто
	// тумблер не трогал, порт закроется молча.
	OpenFirewall     *bool             `json:"openFirewall"`
	RelayMode        string            `json:"relayMode"`
	RawListen        string            `json:"rawListen"`
	DirectListen     string            `json:"directListen"`
	NdmsIface        string            `json:"ndmsIface"`
	WgIface          string            `json:"wgIface"`
	RawNdmsIface     string            `json:"rawNdmsIface"`
	RawIface         string            `json:"rawIface"`
	ExposeToPolicies bool              `json:"exposeToPolicies"`
	Debug            bool              `json:"debug"`        // Г-1 №3
	Clients          []oldServerClient `json:"clients"`      // B5
	LinkPeer         string            `json:"linkPeer"`     // зам. 4
	LinkVKHashes     string            `json:"linkVkHashes"` // зам. 4
	StatsLog         string            `json:"statsLog"`     // зам. 4
}

type oldFreeturnFile struct {
	Version int `json:"version"`
	Clients []struct {
		ID     string            `json:"id"`
		Name   string            `json:"name"`
		Config oldFreeturnClient `json:"config"`
	} `json:"clients"`
	Servers []struct {
		ID     string            `json:"id"`
		Name   string            `json:"name"`
		Config oldFreeturnServer `json:"config"`
	} `json:"servers"`
	// Legacy v1 (до 2026-07-21): singular-поля; миграцию v1→v2 делал ТОЛЬКО
	// старый Store.Load (migrate.go) — посев обязан прочитать их сам (B6).
	Client *oldFreeturnClient `json:"client"`
	Server *oldFreeturnServer `json:"server"`
}

type oldFreeturnClient struct {
	Enabled        bool   `json:"enabled"`
	Listen         string `json:"listen"`
	Peer           string `json:"peer"`
	Provider       string `json:"provider"`
	Links          string `json:"links"`
	Streams        int    `json:"streams"`
	Transport      string `json:"transport"`
	Mode           string `json:"mode"`
	Bond           bool   `json:"bond"`
	TurnHost       string `json:"turnHost"`
	TurnPort       int    `json:"turnPort"`
	ObfProfile     string `json:"obfProfile"`
	ObfKey         string `json:"obfKey"`
	StreamsPerCred int    `json:"streamsPerCred"`
	Platform       string `json:"platform"`
	DNSMode        string `json:"dnsMode"`
	DNSServers     string `json:"dnsServers"`
	ClientID       string `json:"clientId"`
	Sub            string `json:"sub"`
	Debug          bool   `json:"debug"`
}

type oldFreeturnServer struct {
	Enabled     bool   `json:"enabled"`
	Listen      string `json:"listen"`
	Connect     string `json:"connect"`
	Mode        string `json:"mode"`
	ObfProfile  string `json:"obfProfile"`
	ObfKey      string `json:"obfKey"`
	ClientsFile string `json:"clientsFile"`
	Debug       bool   `json:"debug"`
	// OpenFirewall — та же семантика «nil = true» (freeturn/types.go:70-72).
	OpenFirewall *bool `json:"openFirewall"`
}

// openFirewall — перевод тумблера старого мира в новый bool: отсутствующий
// или null-ключ означает ВКЛЮЧЕНО. Одна функция на обе серверные роли, чтобы
// семантика не разъехалась по двум местам.
func openFirewall(old *bool) bool { return old == nil || *old }

// readOldFile — fail-closed чтение одного старого файла. ok=false — файла
// нет (законная чистая установка этой подсистемы).
func readOldFile(path string, dst any) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("посев: читать %s: %w", path, err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return false, fmt.Errorf("посев: разобрать %s: %w", path, err)
	}
	return true, nil
}

// parseOpkgIndex — "OpkgTun18" → (18, true). Ок-флагом, не сентинелом:
// OpkgTun0 — законное имя (Щ13).
func parseOpkgIndex(name string) (int, bool) {
	const p = "OpkgTun"
	if !strings.HasPrefix(name, p) {
		return 0, false
	}
	n, err := strconv.Atoi(name[len(p):])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// normalizeFreeturnV1 — паритет старого мигратора (migrate.go:19-49):
// триггер — version < 2 (НЕ наличие ключей: старый Load создавал дефолтные
// default-инстансы и при отсутствующих singular-полях — оговорка B6 ревьюера
// В). v2-файл с законно пустыми списками не трогается: новый мир разрешает
// ноль инстансов.
func normalizeFreeturnV1(f *oldFreeturnFile) {
	if f.Version >= 2 {
		return
	}
	if len(f.Clients) == 0 {
		c := oldFreeturnClient{Listen: "127.0.0.1:9000", Provider: "vk",
			Streams: 10, Transport: "tcp", Mode: "udp", ObfProfile: "none",
			StreamsPerCred: 10, Platform: "desktop", DNSMode: "auto"}
		if f.Client != nil && *f.Client != (oldFreeturnClient{}) {
			c = *f.Client
		}
		name := "Клиент"
		if c.Peer != "" {
			name = c.Peer
		}
		f.Clients = append(f.Clients, struct {
			ID     string            `json:"id"`
			Name   string            `json:"name"`
			Config oldFreeturnClient `json:"config"`
		}{ID: "default", Name: name, Config: c})
	}
	if len(f.Servers) == 0 {
		s := oldFreeturnServer{Listen: "0.0.0.0:56000", Mode: "udp", ObfProfile: "none"}
		if f.Server != nil && *f.Server != (oldFreeturnServer{}) {
			s = *f.Server
		}
		name := "Сервер"
		if s.Listen != "" {
			name = s.Listen
		}
		f.Servers = append(f.Servers, struct {
			ID     string            `json:"id"`
			Name   string            `json:"name"`
			Config oldFreeturnServer `json:"config"`
		}{ID: "default", Name: name, Config: s})
	}
}

// Seed — посев §9. Идемпотентен: подтверждённый посев — no-op. Fail-closed:
// любой отказ — ошибка всего посева, store не тронут (требование 1 плана 4).
func Seed(ctx context.Context, st *Store, d SeedDeps) (SeedResult, error) {
	cur, err := st.Load()
	if err != nil {
		return SeedResult{}, err
	}
	if cur.Seeded {
		// Посев — no-op, но недоведённая уборка обязана повториться: pid'ы
		// старого поколения пересобираются заново (pid-файлы на месте), список
		// прежних kernel-имён приходит с диска.
		res := SeedResult{State: cur, CleanupPending: cur.CleanupPending,
			LegacyKernelIfaces: cur.LegacyKernelIfaces}
		if cur.CleanupPending {
			res.OldGenPIDs = oldGenerationPIDs(d.RuntimeDir)
		}
		return res, nil
	}

	var wdttFile oldWdttFile
	var ftFile oldFreeturnFile
	wdttOK, err := readOldFile(d.WdttPath, &wdttFile)
	if err != nil {
		return SeedResult{}, err
	}
	ftOK, err := readOldFile(d.FreeturnPath, &ftFile)
	if err != nil {
		return SeedResult{}, err
	}
	if ftOK {
		normalizeFreeturnV1(&ftFile) // B6: триггер version<2 — внутри
	}

	idxMin, idxMax, _ := roles.OpkgIndexRange(d.GOARCH)
	var seeded []Record
	var legacyIfaces []string

	// ensurePin — валидный пин: свой, если он в диапазоне, иначе новый
	// (перепин mips, требование (7в) плана 3). keptLive — выдан ровно
	// запрошенный пин: только тогда можно спрашивать live-permits (у
	// перепиненного старого интерфейса не существовало).
	ensurePin := func(owner, ndmsName string) (ndms, kernel string, keptLive bool, err error) {
		pinned, havePin := parseOpkgIndex(strings.TrimSpace(ndmsName))
		if havePin && (pinned < idxMin || pinned > idxMax) {
			havePin = false // недостижимый на этой архитектуре — перепин
		}
		idx, err := d.AllocIndex(owner, pinned, havePin)
		if err != nil {
			return "", "", false, fmt.Errorf("посев: нет свободного OpkgTun для %s: %w", owner, err)
		}
		return fmt.Sprintf("OpkgTun%d", idx), fmt.Sprintf("opkgtun%d", idx),
			havePin && idx == pinned, nil
	}

	for _, c := range wdttFile.Clients {
		cfg := roles.WdttClientConfig{
			Mode: strings.TrimSpace(c.Config.ConnMode), Listen: c.Config.Listen,
			Password: c.Config.Password, VKHashes: c.Config.VKHashes,
			Workers: c.Config.Workers, Obfs: c.Config.Obfs,
			Fingerprint: c.Config.Fingerprint, DeviceID: c.Config.DeviceID,
			CaptchaMode: c.Config.CaptchaMode, VKAuthMode: c.Config.VKAuthMode,
		}
		if cfg.Mode == "" {
			cfg.Mode = "wg"
		}
		// Peer — слот активного режима, фолбэк общий peer (паритет
		// normalizePeers старого мира).
		slot := c.Config.PeerWg
		if cfg.Mode == "raw" {
			slot = c.Config.PeerRaw
		}
		if s := strings.TrimSpace(slot); s != "" {
			cfg.Peer = s
		} else {
			cfg.Peer = strings.TrimSpace(c.Config.Peer)
		}
		if cfg.Mode == "raw" {
			owner := string(KindWdttClient) + ":" + c.ID
			ndms, kernel, keptLive, err := ensurePin(owner, c.Config.NdmsIface)
			if err != nil {
				return SeedResult{}, err
			}
			cfg.NdmsIface, cfg.RawIface = ndms, kernel
			// Намерение членства = live ∪ cache (§9). Ошибка наблюдения —
			// отказ ВСЕГО посева («флаг только по успешному наблюдению»).
			var live []string
			if keptLive {
				live, err = d.LivePermits(ctx, ndms)
				if err != nil {
					return SeedResult{}, fmt.Errorf("посев: наблюдение политик %s: %w", ndms, err)
				}
			}
			// Кэш несёт СТАРЫЙ order — приоритет кандидатуры default route
			// (Г-1 №2); live-довесок идёт с незакреплённой позицией (nil, в
			// хвост): его место уже стоит в живой политике.
			seen := map[string]bool{}
			for _, p := range c.Config.PolicyPermits {
				if p.Name == "" || seen[p.Name] {
					continue
				}
				seen[p.Name] = true
				// Отображаем ЛЮБОЙ order, включая ноль: старый формат писал
				// поле без omitempty, и `"order": 0` на диске означает верх
				// политики — выход, поднятый пользователем выше провайдера.
				// Условие «order > 0» увело бы его в хвост молча.
				order := p.Order
				cfg.Policies = append(cfg.Policies, roles.PolicyPermit{Name: p.Name, Order: &order})
			}
			for _, p := range live {
				if !seen[p] && p != "" {
					seen[p] = true
					cfg.Policies = append(cfg.Policies, roles.PolicyPermit{Name: p})
				}
			}
		}
		seeded = append(seeded, Record{ID: c.ID, Kind: KindWdttClient, Name: c.Name,
			Enabled: c.Config.Enabled, Sub: c.Config.Sub,
			// Г-1 №1: оба слота — фронт восстанавливает адрес при
			// переключении режима; инвариант дальше держит normalizeRecord.
			PeerWg: c.Config.PeerWg, PeerRaw: c.Config.PeerRaw,
			WdttClient: &cfg})
	}

	for _, s := range wdttFile.Servers {
		o := s.Config
		cfg := roles.WdttServerConfig{
			Listen: o.Listen, WgPort: o.WgPort, ConfigDir: o.ConfigDir,
			Password: o.Password, AdminID: o.AdminID, BotToken: o.BotToken,
			NatIface: o.NatIface, NatMode: o.NatMode, NatStaticWAN: o.NatStaticWAN,
			Policy: o.Policy, LanSegments: o.LanSegments,
			RelayMode: o.RelayMode, RawListen: o.RawListen, DirectListen: o.DirectListen,
			ExposeToPolicies: o.ExposeToPolicies,
			Debug:            o.Debug, // Г-1 №3: тумблер пользователя
			OpenFirewall:     openFirewall(o.OpenFirewall),
		}
		if strings.TrimSpace(cfg.ConfigDir) == "" {
			// Г-1 №4: фиксируем СТАРУЮ форму пути (wdtt/server.go:362-366) —
			// файл абонентов с выданными IP не должен «переехать» на апгрейде.
			cfg.ConfigDir = filepath.Join(filepath.Dir(d.WdttPath), "wdtt", "server", s.ID)
		}
		owner := string(KindWdttServer) + ":" + s.ID
		// Прежние kernel-имена — на одноразовую уборку правил; legacy-мир
		// без NDMS-имён жил на wdtt0/wdttraw0.
		if o.WgIface != "" {
			legacyIfaces = append(legacyIfaces, o.WgIface)
		} else {
			legacyIfaces = append(legacyIfaces, "wdtt0")
		}
		if o.RawIface != "" {
			legacyIfaces = append(legacyIfaces, o.RawIface)
		} else {
			legacyIfaces = append(legacyIfaces, "wdttraw0")
		}
		ndmsWG, kernWG, _, err := ensurePin(owner+"/wg", o.NdmsIface)
		if err != nil {
			return SeedResult{}, err
		}
		cfg.NdmsIface, cfg.WgIface = ndmsWG, kernWG
		ndmsRaw, kernRaw, _, err := ensurePin(owner+"/raw", o.RawNdmsIface)
		if err != nil {
			return SeedResult{}, err
		}
		cfg.RawNdmsIface, cfg.RawIface = ndmsRaw, kernRaw
		rec := Record{ID: s.ID, Kind: KindWdttServer, Name: s.Name,
			Enabled: o.Enabled, WdttServer: &cfg,
			// B5 + замечание 4: пользовательские данные сервера.
			LinkPeer: o.LinkPeer, LinkVKHashes: o.LinkVKHashes, StatsLog: o.StatsLog}
		for _, u := range o.Clients {
			rec.Users = append(rec.Users, ServerUser{Password: u.Password,
				Comment: u.Comment, VkHash: u.VkHash, ExpiresAt: u.ExpiresAt, Auto: u.Auto})
		}
		seeded = append(seeded, rec)
	}

	for _, c := range ftFile.Clients {
		o := c.Config
		seeded = append(seeded, Record{ID: c.ID, Kind: KindFreeTurnClient, Name: c.Name,
			Enabled: o.Enabled, FreeTurnClient: &roles.FreeTurnClientConfig{
				Listen: o.Listen, Peer: o.Peer, Provider: o.Provider, Links: o.Links,
				Streams: o.Streams, Transport: o.Transport, Mode: o.Mode, Bond: o.Bond,
				TurnHost: o.TurnHost, TurnPort: o.TurnPort,
				ObfProfile: o.ObfProfile, ObfKey: o.ObfKey,
				StreamsPerCred: o.StreamsPerCred, Platform: o.Platform,
				DNSMode: o.DNSMode, DNSServers: o.DNSServers,
				ClientID: o.ClientID, Sub: o.Sub, Debug: o.Debug, // Sub — B4
			}})
	}
	for _, s := range ftFile.Servers {
		o := s.Config
		seeded = append(seeded, Record{ID: s.ID, Kind: KindFreeTurnServer, Name: s.Name,
			Enabled: o.Enabled, FreeTurnServer: &roles.FreeTurnServerConfig{
				Listen: o.Listen, Connect: o.Connect, Mode: o.Mode,
				ObfProfile: o.ObfProfile, ObfKey: o.ObfKey, ClientsFile: o.ClientsFile,
				Debug: o.Debug, OpenFirewall: openFirewall(o.OpenFirewall),
			}})
	}

	from := []string{}
	if wdttOK {
		from = append(from, filepath.Base(d.WdttPath))
	}
	if ftOK {
		from = append(from, filepath.Base(d.FreeturnPath))
	}
	if len(from) == 0 {
		from = []string{"clean-install"}
	}

	next, err := st.Replace(func(state *State) error {
		exists := map[string]bool{}
		for _, r := range state.Records {
			exists[r.Key()] = true
		}
		for _, r := range seeded {
			if exists[r.Key()] {
				continue // существующая запись главнее посева
			}
			state.Records = append(state.Records, r)
		}
		state.SeededFrom = from
		state.CleanupPending = true
		state.LegacyKernelIfaces = legacyIfaces
		return nil
	})
	if err != nil {
		return SeedResult{}, err
	}

	return SeedResult{
		State:              next,
		SeededNow:          true,
		CleanupPending:     true,
		OldGenPIDs:         oldGenerationPIDs(d.RuntimeDir),
		LegacyKernelIfaces: legacyIfaces,
	}, nil
}

// ClearCleanupPending — снятие отметки ОТДЕЛЬНОЙ транзакцией, после успешного
// прохода одноразовых шагов. Список прежних kernel-имён уходит вместе с ней:
// он существует только ради этих шагов.
func ClearCleanupPending(st *Store) error {
	_, err := st.Replace(func(state *State) error {
		state.CleanupPending = false
		state.LegacyKernelIfaces = nil
		return nil
	})
	return err
}

// oldGenerationPIDs — pid-файлы старого мира (форма имени —
// wdtt/process.go:72). Best-effort: нечитаемое пропускается; проверку
// живости и принадлежности бинарю делает адаптер kill (задача 6, B3).
func oldGenerationPIDs(runtimeDir string) []int {
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".pid") {
			continue
		}
		if !strings.HasPrefix(name, "wdtt-") && !strings.HasPrefix(name, "freeturn-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(runtimeDir, name))
		if err != nil {
			continue
		}
		// pid <= 0 отбрасывается не для красоты: ноль и минус в kill(2)
		// означают группу процессов — «добивание» такого «pid» убило бы
		// посторонних.
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 0 {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}
