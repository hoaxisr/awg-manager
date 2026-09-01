package instancestore

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/childproc"
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

// OldGenProc — процесс старого поколения: номер и ОТПЕЧАТОК (время старта,
// поле 22 /proc/<pid>/stat). Голый номер идентичностью не является: pid-файлы
// старого мира лежат на флеше, переживают перезагрузку и никем не удаляются,
// а номер система переиспользует. Сверка по имени бинаря тут бессильна —
// бинарь у старого и нового поколения ОДИН И ТОТ ЖЕ, то есть добивание по
// голому номеру могло убить усыновлённый процесс нового мира.
//
// StartTime=0 — отпечаток снять не удалось (процесса уже не было). Добивать
// такой номер нельзя: к моменту прохода он мог достаться кому угодно.
type OldGenProc struct {
	PID       int    `json:"pid"`
	StartTime uint64 `json:"startTime,omitempty"`
}

type SeedResult struct {
	State     State
	SeededNow bool
	// CleanupPending — одноразовые шаги не доведены: либо посев только что
	// состоялся, либо прошлый проход не удался и отметка на диске висит.
	CleanupPending bool
	// OldGenProcs — кандидаты на добивание (§9 протокола). Только собраны:
	// живость, отпечаток и принадлежность бинарю проверяет адаптер (задача 6,
	// B3 — pid-файл на флешке переживает ребут, PID мог достаться
	// постороннему).
	OldGenProcs []OldGenProc
	// LegacyKernelIfaces — прежние kernel-имена сервера: вход одноразовой
	// уборки непомеченных правил (план 3, residual I-1(а)).
	LegacyKernelIfaces []string
	// MovedListen — инстансы, которым СМЕНИЛИ listen-адрес, разводя
	// конфликт за порт. Источников ЧЕТЫРЕ: посев (у подсистем совпадал
	// дефолтный порт), боот (порт отняла чужая запись), создание и правка
	// инстанса — не только посев, как говорила прежняя редакция. Наружу — ради
	// журнала и признака в поверхности статуса: снаружи мог быть настроен
	// клиент на прежний порт.
	MovedListen []ListenMove
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
	Enabled      bool   `json:"enabled"`
	Listen       string `json:"listen"`
	WgPort       int    `json:"wgPort"`
	ConfigDir    string `json:"configDir"`
	NatMode      string `json:"natMode"`
	NatStaticWAN string `json:"natStaticWan"`
	// NatStaticWANs — форма списка (develop, PR #750). Старые записи её не
	// имеют: у них заполнена одиночка выше, и StaticNATList сводит обе.
	NatStaticWANs []string `json:"natStaticWans"`
	Policy        string   `json:"policy"`
	LanSegments   []string `json:"lanSegments"`
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

// readOldFile — чтение одного старого файла. ok=false без причины — файла нет
// (законная чистая установка этой подсистемы).
//
// Два исхода отказа РАЗНЫЕ. Ошибка ввода-вывода — состояние среды: оно
// самоизлечивается, и повтор на следующем бооте правилен, поэтому она
// остаётся фатальной. Неразбираемый JSON не починится сам никогда: отказ
// посева на нём не поднимал бы прокси-подсистему вовсе, и пользователь не мог
// бы даже пересоздать инстансы руками — интерфейса нет. Поэтому это ПРОПУСК с
// причиной, а не ошибка.
func readOldFile(path string, dst any) (ok bool, skipped string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("посев: читать %s: %w", path, err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return false, err.Error(), nil
	}
	return true, "", nil
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

// normalizeFreeturnV1 — подъём singular-полей v1 (до 2026-07-21) в списки:
// миграцию v1→v2 делал ТОЛЬКО старый Store.Load (migrate.go), и без неё
// настройки таких файлов потерялись бы молча.
//
// Расхождение со старым мигратором осознанное (амендмент G1, решение
// владельца): пустой singular-конфиг НЕ достраивается инстансом из дефолтов.
// Старый мир синтезировал клиента с Listen 127.0.0.1:9000 из воздуха, и
// именно эта пустышка после слияния подсистем становилась вторым претендентом
// на порт у людей, которые freeturn никогда не настраивали. Пустой конфиг —
// это «переносить нечего»; кому нужен инстанс, создаст его кнопкой.
func normalizeFreeturnV1(f *oldFreeturnFile) {
	if f.Version >= 2 {
		return
	}
	if len(f.Clients) == 0 && f.Client != nil && *f.Client != (oldFreeturnClient{}) {
		c := *f.Client
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
	if len(f.Servers) == 0 && f.Server != nil && *f.Server != (oldFreeturnServer{}) {
		s := *f.Server
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

// listenPortOf — порт listen-адреса, ЛЮБОЙ хост. Сравнение по строке было бы
// слепым: 127.0.0.1:9000 и 0.0.0.0:9000 — один и тот же занятый порт роутера.
func listenPortOf(addr string) (int, bool) {
	_, p, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return 0, false
	}
	return n, true
}

// listenAddrs — все адреса прослушивания записи. Серверные RawListen и
// DirectListen тоже здесь: порт роутера они занимают наравне с остальными, и
// выдать его переезжающему клиенту нельзя.
func listenAddrs(r Record) []string {
	switch {
	case r.WdttClient != nil:
		return []string{r.WdttClient.Listen}
	case r.FreeTurnClient != nil:
		return []string{r.FreeTurnClient.Listen}
	case r.WdttServer != nil:
		return []string{r.WdttServer.Listen, r.WdttServer.RawListen, r.WdttServer.DirectListen}
	case r.FreeTurnServer != nil:
		return []string{r.FreeTurnServer.Listen}
	}
	return nil
}

// clientListen — адрес клиентской роли ПО ССЫЛКЕ (конфиг лежит за указателем,
// поэтому запись можно передавать значением). nil — роль неподвижна: у
// серверов listen это WAN-порт, на который настроены проброс и внешние
// клиенты, и пул 9000..9200 не про него.
// ClientListen — указатель на локальный listen клиента внутри записи; nil у
// серверных ролей (их listen задаёт пользователь). Экспортирован ради боота
// менеджера: тот сверяет порты с занятостью и переселяет негодные.
//
// Единственная развилка по ролям: вторая копия рано или поздно разъедется с
// этой, и новая клиентская роль молча выпала бы из одного из двух путей.
func ClientListen(r *Record) *string {
	switch {
	case r.WdttClient != nil:
		return &r.WdttClient.Listen
	case r.FreeTurnClient != nil:
		return &r.FreeTurnClient.Listen
	}
	return nil
}

// clientListen — та же развилка для значения записи (посев работает с копиями).
func clientListen(r Record) *string { return ClientListen(&r) }

// resolveListenConflicts — разведение претендентов на один порт (амендмент G2).
//
// Дефолт listen у обеих подсистем был ОДИН И ТОТ ЖЕ (127.0.0.1:9000), и в
// старом мире это не было видно: подсистемы жили порознь, каждая знала только
// свои порты. После слияния оба клиента претендуют на порт, ресурс listen_port
// отказывает второму, и его зависимые ресурсы уходят в blocked — то есть
// клиент не поднимается. Сценарий массовый, а не частный.
//
// Правило владельца: порт держит ВКЛЮЧЁННЫЙ инстанс; при равенстве (оба
// включены либо оба выключены) — wdtt-клиент. Проигравший получает свободный
// порт из пула. Возвращает переезды: молча сменить порт нельзя, снаружи может
// быть настроен клиент на прежний.
//
// Только посев: дальше пустой Listen заполняет единый аллокатор
// (manager.ensurePins → AllocListen), и занятый порт он не выдаст.
func resolveListenConflicts(recs []Record) []ListenMove {
	taken := map[int]bool{}
	for _, r := range recs {
		if clientListen(r) != nil {
			continue // подвижные разбираются ниже, по приоритету
		}
		for _, a := range listenAddrs(r) {
			if p, ok := listenPortOf(a); ok {
				taken[p] = true
			}
		}
	}

	order := make([]int, 0, len(recs))
	for i := range recs {
		if clientListen(recs[i]) != nil {
			order = append(order, i)
		}
	}
	// Stable, а не Slice: при полном равенстве (два клиента одной роли с
	// одинаковым Enabled) порт остаётся за тем, кто раньше в старом конфиге, —
	// иначе выбор жертвы зависел бы от прогона.
	sort.SliceStable(order, func(a, b int) bool {
		ra, rb := recs[order[a]], recs[order[b]]
		if ra.Enabled != rb.Enabled {
			return ra.Enabled
		}
		return ra.Kind == KindWdttClient && rb.Kind != KindWdttClient
	})

	var moves []ListenMove
	for _, i := range order {
		listen := clientListen(recs[i])
		port, ok := listenPortOf(*listen)
		if !ok {
			continue // адрес не разбирается — разводить нечего, это приговор ресурса
		}
		if !taken[port] {
			taken[port] = true
			continue
		}
		free := 0
		for p := roles.ListenPortMin; p <= roles.ListenPortMax && free == 0; p++ {
			if !taken[p] {
				free = p
			}
		}
		if free == 0 {
			// Пул выбран целиком: переселять некуда. Конфликт остаётся, и о нём
			// скажет ресурс listen_port — тихой потери здесь нет.
			continue
		}
		taken[free] = true
		from := *listen
		*listen = fmt.Sprintf("127.0.0.1:%d", free)
		moves = append(moves, ListenMove{Instance: recs[i].Key(), Name: recs[i].Name,
			From: from, To: *listen})
	}
	return moves
}

// Seed — посев §9. Идемпотентен: подтверждённый посев — no-op. Fail-closed:
// любой отказ — ошибка всего посева, store не тронут (требование 1 плана 4).
func Seed(ctx context.Context, st *Store, d SeedDeps) (SeedResult, error) {
	cur, err := st.Load()
	if err != nil {
		return SeedResult{}, err
	}
	if cur.Seeded {
		// Посев — no-op, но недоведённая уборка обязана повториться: оба её
		// списка приходят с диска. Номера повтор прочитал бы и сам — те же
		// pid-файлы никто не удаляет, — а вот ОТПЕЧАТКИ снимаются только на
		// посеве, пока старое поколение ещё живо. Без них добивание не
		// отличит процесс старого мира от чужого с тем же номером.
		return SeedResult{State: cur, CleanupPending: cur.CleanupPending,
			LegacyKernelIfaces: cur.LegacyKernelIfaces,
			OldGenProcs:        cur.OldGenProcs,
			MovedListen:        cur.MovedListen}, nil
	}

	var wdttFile oldWdttFile
	var ftFile oldFreeturnFile
	var skipped []SkippedSource
	wdttOK, wdttSkip, err := readOldFile(d.WdttPath, &wdttFile)
	if err != nil {
		return SeedResult{}, err
	}
	if wdttSkip != "" {
		// Обнуление обязательно: encoding/json оставляет в приёмнике то, что
		// успел разобрать до отказа, и половина файла уехала бы в записи.
		wdttFile = oldWdttFile{}
		skipped = append(skipped, SkippedSource{File: filepath.Base(d.WdttPath), Reason: wdttSkip})
	}
	ftOK, ftSkip, err := readOldFile(d.FreeturnPath, &ftFile)
	if err != nil {
		return SeedResult{}, err
	}
	if ftSkip != "" {
		ftFile = oldFreeturnFile{}
		skipped = append(skipped, SkippedSource{File: filepath.Base(d.FreeturnPath), Reason: ftSkip})
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

	// Имя источника ложится в КАЖДУЮ перенесённую запись: по нему UI показывает
	// бейдж «перенесено». Считается из пути, как и `State.SeededFrom`.
	wdttSrc := filepath.Base(d.WdttPath)
	ftSrc := filepath.Base(d.FreeturnPath)

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
			Enabled: c.Config.Enabled, Sub: c.Config.Sub, SeededFrom: wdttSrc,
			// Г-1 №1: оба слота — фронт восстанавливает адрес при
			// переключении режима; инвариант дальше держит normalizeRecord.
			PeerWg: c.Config.PeerWg, PeerRaw: c.Config.PeerRaw,
			WdttClient: &cfg})
	}

	for _, s := range wdttFile.Servers {
		o := s.Config
		cfg := roles.WdttServerConfig{
			Listen: o.Listen, WgPort: o.WgPort, ConfigDir: o.ConfigDir,
			NatMode: o.NatMode, NatStaticWAN: o.NatStaticWAN,
			NatStaticWANs: o.NatStaticWANs,
			Policy:        o.Policy, LanSegments: o.LanSegments,
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
			Enabled: o.Enabled, WdttServer: &cfg, SeededFrom: wdttSrc,
			// B5 + замечание 4: пользовательские данные сервера.
			LinkPeer: o.LinkPeer, LinkVKHashes: o.LinkVKHashes, StatsLog: o.StatsLog}
		for _, u := range o.Clients {
			rec.Users = append(rec.Users, ServerUser{Password: u.Password,
				Comment: u.Comment, VkHash: u.VkHash, Auto: u.Auto})
		}
		seeded = append(seeded, rec)
	}

	for _, c := range ftFile.Clients {
		o := c.Config
		seeded = append(seeded, Record{ID: c.ID, Kind: KindFreeTurnClient, Name: c.Name,
			Enabled: o.Enabled, SeededFrom: ftSrc, FreeTurnClient: &roles.FreeTurnClientConfig{
				Listen: o.Listen, Peer: o.Peer, Provider: o.Provider, Links: o.Links,
				Streams: o.Streams, Transport: o.Transport, Mode: o.Mode, Bond: o.Bond,
				ObfProfile: o.ObfProfile, ObfKey: o.ObfKey,
				StreamsPerCred: o.StreamsPerCred, Platform: o.Platform,
				DNSMode: o.DNSMode, DNSServers: o.DNSServers,
				ClientID: o.ClientID, Sub: o.Sub, Debug: o.Debug, // Sub — B4
			}})
	}
	for _, s := range ftFile.Servers {
		o := s.Config
		seeded = append(seeded, Record{ID: s.ID, Kind: KindFreeTurnServer, Name: s.Name,
			Enabled: o.Enabled, SeededFrom: ftSrc, FreeTurnServer: &roles.FreeTurnServerConfig{
				Listen: o.Listen, Connect: o.Connect, Mode: o.Mode,
				ObfProfile: o.ObfProfile, ObfKey: o.ObfKey, ClientsFile: o.ClientsFile,
				Debug: o.Debug, OpenFirewall: openFirewall(o.OpenFirewall),
			}})
	}

	// Разведение портов идёт ПОСЛЕ сборки всех ролей: правило приоритета
	// сравнивает претендентов между собой, и знать их всех надо разом.
	moves := resolveListenConflicts(seeded)

	from := []string{}
	if wdttOK {
		from = append(from, filepath.Base(d.WdttPath))
	}
	if ftOK {
		from = append(from, filepath.Base(d.FreeturnPath))
	}
	if len(from) == 0 {
		// Переносить нечего — источников нет ЛИБО все они пропущены как
		// неразбираемые. Отметка ложится всё равно: Seeded выводится из
		// SeededFrom, и пустой список гнал бы посев по второму кругу на каждом
		// бооте — тот самый бесконечный луп, ради которого пропуск и заведён.
		from = []string{"clean-install"}
	}

	oldGenProcs := oldGenerationProcs(d.RuntimeDir)
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
		state.SkippedSources = skipped
		state.MovedListen = moves
		state.CleanupPending = true
		state.LegacyKernelIfaces = legacyIfaces
		state.OldGenProcs = oldGenProcs
		return nil
	})
	if err != nil {
		return SeedResult{}, err
	}

	return SeedResult{
		State:              next,
		SeededNow:          true,
		CleanupPending:     true,
		OldGenProcs:        oldGenProcs,
		LegacyKernelIfaces: legacyIfaces,
		MovedListen:        moves,
	}, nil
}

// ClearCleanupPending — снятие отметки ОТДЕЛЬНОЙ транзакцией, после успешного
// прохода одноразовых шагов. Оба списка уходят вместе с ней: они существуют
// только ради этих шагов.
func ClearCleanupPending(st *Store) error {
	_, err := st.Replace(func(state *State) error {
		state.CleanupPending = false
		state.LegacyKernelIfaces = nil
		state.OldGenProcs = nil
		return nil
	})
	return err
}

// oldGenerationProcs — pid-файлы старого мира (форма имени —
// wdtt/process.go:72) вместе с отпечатком каждого номера. Отпечаток снимается
// ЗДЕСЬ, на посеве, пока процессы старого поколения ещё живы: на повторном
// проходе уборки номер уже мог быть переиспользован. Best-effort: нечитаемое
// пропускается; живость и принадлежность бинарю проверяет адаптер kill.
func oldGenerationProcs(runtimeDir string) []OldGenProc {
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		return nil
	}
	var procs []OldGenProc
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
		start, _ := childproc.StartTime(pid) // 0 — процесса уже нет
		procs = append(procs, OldGenProc{PID: pid, StartTime: start})
	}
	return procs
}
