package zapret

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxHostlistBytes — предохранитель на размер редактируемого списка.
// Реальные списки RKN/YouTube весят сотни килобайт, так что 2 МиБ —
// с большим запасом; больше означает, что открыли не тот файл.
const maxHostlistBytes = 2 << 20

const maxHostlistCountCacheEntries = 1024

type hostlistCounts struct {
	entries int
	ips     int
}

type hostlistCountCacheEntry struct {
	size    int64
	modTime time.Time
	counts  hostlistCounts
}

var hostlistCountCache = struct {
	sync.Mutex
	entries map[string]hostlistCountCacheEntry
}{entries: make(map[string]hostlistCountCacheEntry)}

// HostlistID — идентификатор списка в API. Не путь: путь приходит от
// клиента только на запись, а ID задаёт сервер, поэтому подсунуть
// произвольный файл нельзя.
type HostlistID string

const (
	// HostlistUser — zapret-hosts-user.txt. Единственный список, который
	// пользователь обязан вести сам; ни один скрипт zapret и его форков
	// его не переписывает.
	HostlistUser HostlistID = "user"
	// HostlistExclude — zapret-hosts-user-exclude.txt. Тоже
	// пользовательский: upstream только кладёт рядом .default-образец.
	HostlistExclude HostlistID = "exclude"
	// HostlistAuto — zapret-hosts-auto.txt. Пишет сам nfqws2 по итогам
	// детекта блокировок; его можно подрезать, но он же наполняется
	// заново, поэтому AWGM показывает его и считает, но не правит.
	HostlistAuto HostlistID = "auto"
	// HostlistMain — zapret-hosts.txt. Пересобирается ночью скриптом
	// get_antizapret_domains.sh из внешних источников.
	HostlistMain HostlistID = "main"
)

// HostlistInfo — описание одного списка для UI.
type HostlistInfo struct {
	ID     HostlistID `json:"id"`
	Path   string     `json:"path"`
	Exists bool       `json:"exists"`
	// Entries — число непустых строк (минус комментарии).
	Entries int `json:"entries"`
	// Domains/IPs — разбор содержимого. Для zapret-hosts-user.txt это
	// важно: IP там разрешаются в отдельный список ipset'ом, но сами
	// демоны в MODE_FILTER=hostlist их не видят.
	Domains   int       `json:"domains"`
	IPs       int       `json:"ips"`
	Editable  bool      `json:"editable"`
	Gzipped   bool      `json:"gzipped"`
	Generated bool      `json:"generated"`
	Warnings  []Warning `json:"warnings,omitempty"`
}

// hostlistSpec — описание списка: где лежит, можно ли править, кто его
// перезаписывает.
type hostlistSpec struct {
	id        HostlistID
	filename  string
	editable  bool
	generated bool
}

func hostlistSpecs() []hostlistSpec {
	return []hostlistSpec{
		{id: HostlistUser, filename: "zapret-hosts-user.txt", editable: true},
		{id: HostlistExclude, filename: "zapret-hosts-user-exclude.txt", editable: true},
		{id: HostlistAuto, filename: "zapret-hosts-auto.txt", generated: true},
		{id: HostlistMain, filename: "zapret-hosts.txt", generated: true},
	}
}

// resolveHostlistFile ищет файл списка с учётом .gz. zapret при GZIP_LISTS=1
// (дефолт) сжимает списки, но .gz-версии генерируемых файлов читает и
// создаёт сам; пользовательские остаются обычными, потому что писать их
// некому. Пробуем .gz первым — так же, как это делает find_hostlists()
// в common/list.sh: при наличии обоих zapret возьмёт .gz.
func resolveHostlistFile(dir string, filename string) (path string, gzipped bool) {
	gz := filepath.Join(dir, filename+".gz")
	if st, err := os.Stat(gz); err == nil && st.Size() > 0 {
		return gz, true
	}
	plain := filepath.Join(dir, filename)
	if _, err := os.Stat(plain); err == nil {
		return plain, false
	}
	return plain, false
}

// newHostlistRef строит ссылку на список для карточки стратегии.
func newHostlistRef(path string) StrategyHostlist {
	ref := StrategyHostlist{Path: path}
	st, err := os.Stat(path)
	if err != nil {
		return ref
	}
	ref.Exists = true
	counts, err := countHostlist(path, st)
	if err == nil {
		ref.Entries = counts.entries
		ref.HasIPs = counts.ips > 0
		ref.IPOnly = ref.HasIPs && ref.Entries == counts.ips
	} else {
		ref.countErr = err.Error()
	}
	return ref
}

// countHostlist counts entries and IPs in one streaming pass. The cache key
// includes both size and modification time so generated lists are refreshed
// as they change without retaining their full contents in memory.
func countHostlist(path string, st os.FileInfo) (hostlistCounts, error) {
	if st == nil {
		var err error
		st, err = os.Stat(path)
		if err != nil {
			return hostlistCounts{}, err
		}
	}
	if !st.Mode().IsRegular() {
		return hostlistCounts{}, fmt.Errorf("%s is not a regular file", path)
	}
	key := filepath.Clean(path)
	hostlistCountCache.Lock()
	cached, ok := hostlistCountCache.entries[key]
	if ok && cached.size == st.Size() && cached.modTime.Equal(st.ModTime()) {
		hostlistCountCache.Unlock()
		return cached.counts, nil
	}
	hostlistCountCache.Unlock()

	file, err := os.Open(path)
	if err != nil {
		return hostlistCounts{}, err
	}
	defer file.Close()

	var reader io.Reader = file
	var zipped *gzip.Reader
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		zipped, err = gzip.NewReader(file)
		if err != nil {
			return hostlistCounts{}, fmt.Errorf("open gzip hostlist %s: %w", path, err)
		}
		defer zipped.Close()
		reader = zipped
	}

	counts := hostlistCounts{}
	sc := bufio.NewReader(reader)
	for {
		line, readErr := sc.ReadString('\n')
		if entryMeaningful(line) {
			counts.entries++
			if looksLikeIP(strings.TrimSpace(line)) {
				counts.ips++
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return hostlistCounts{}, fmt.Errorf("read hostlist %s: %w", path, readErr)
		}
	}

	hostlistCountCache.Lock()
	if len(hostlistCountCache.entries) >= maxHostlistCountCacheEntries {
		clear(hostlistCountCache.entries)
	}
	hostlistCountCache.entries[key] = hostlistCountCacheEntry{
		size: st.Size(), modTime: st.ModTime(), counts: counts,
	}
	hostlistCountCache.Unlock()
	return counts, nil
}

func entryMeaningful(line string) bool {
	line = strings.TrimSpace(line)
	return line != "" && !strings.HasPrefix(line, "#")
}

// looksLikeIP отличает адрес/подсеть от домена. Разбор настоящий, через
// netip: самодельная эвристика по точкам путала IPv6 с hex-буквами
// (2001:db8::1) и домен с четыремя числовыми сегментами, а нам нужно
// точно знать, сработает ли предупреждение «в списке только IP».
// Домен в списке — это либо FQDN, либо адрес/подсеть; трехпрогонная
// проверка покрывает обе формы, не требуя резолва.
func looksLikeIP(entry string) bool {
	if entry == "" {
		return false
	}
	// CIDR: у addr есть ровно одна косая черта, дальше — маска.
	// netip.ParsePrefix сам разбирает и IPv4, и IPv6, и проверяет, что
	// адрес входит в префикс, чего ParseAddr не делает.
	if _, _, found := strings.Cut(entry, "/"); found {
		_, err := netip.ParsePrefix(entry)
		return err == nil
	}
	if _, err := netip.ParseAddr(entry); err == nil {
		return true
	}
	// Хост без маски, например «1.2.3.4» — уже покрыт ParseAddr.
	// Ничего не подошло — домен.
	return false
}

// listHostlists собирает описания всех известных списков.
func listHostlists(l layout) []HostlistInfo {
	specs := hostlistSpecs()
	out := make([]HostlistInfo, 0, len(specs))
	for _, sp := range specs {
		path, gz := resolveHostlistFile(l.ipset, sp.filename)
		info := HostlistInfo{
			ID:        sp.id,
			Path:      path,
			Editable:  sp.editable,
			Generated: sp.generated,
			Gzipped:   gz,
		}
		if st, err := os.Stat(path); err == nil {
			info.Exists = true
			if counts, countErr := countHostlist(path, st); countErr == nil {
				info.Entries = counts.entries
				info.IPs = counts.ips
				info.Domains = info.Entries - info.IPs
			} else {
				info.Warnings = append(info.Warnings, Warning{Code: "hostlist_count_failed", Detail: countErr.Error()})
			}
		}
		if info.Editable && info.Exists && info.IPs > 0 && info.Domains == 0 {
			info.Warnings = append(info.Warnings, Warning{Code: "hostlist_ip_only"})
		}
		out = append(out, info)
	}
	return out
}

// readHostlist отдаёт содержимое списка. .gz не распаковываем в память
// целиком ради редактирования — вместо этого сообщаем, что список
// сжат, и предлагаем править несжатую пару: zapret сам создаст её при
// следующей пересборке.
func readHostlist(l layout, id HostlistID) (path, content string, err error) {
	spec, ok := findSpec(id)
	if !ok {
		return "", "", fmt.Errorf("unknown hostlist %q", id)
	}
	resolved, gz := resolveHostlistFile(l.ipset, spec.filename)
	st, statErr := os.Stat(resolved)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) && spec.editable {
			return resolved, "", nil
		}
		if errors.Is(statErr, os.ErrNotExist) {
			return resolved, "", fmt.Errorf("hostlist %s was not found (%s)", spec.filename, resolved)
		}
		return resolved, "", fmt.Errorf("could not inspect hostlist %s: %w", spec.filename, statErr)
	}
	if gz {
		return resolved, "", fmt.Errorf("%s is compressed as .gz; decompress it before editing", spec.filename)
	}
	if st.Size() > maxHostlistBytes {
		return resolved, "", fmt.Errorf("hostlist %s exceeds %d bytes", spec.filename, int64(maxHostlistBytes))
	}
	data, readErr := os.ReadFile(resolved)
	if readErr != nil {
		return resolved, "", fmt.Errorf("read %s: %w", spec.filename, readErr)
	}
	return resolved, string(data), nil
}

// writeHostlist заменяет содержимое списка.
func (s *Service) writeHostlist(l layout, id HostlistID, content, revision string) (string, error) {
	spec, ok := findSpec(id)
	if !ok {
		return "", fmt.Errorf("unknown hostlist %q", id)
	}
	if !spec.editable {
		return "", fmt.Errorf("hostlist %s is generated automatically and cannot be edited", spec.filename)
	}
	if len(content) > maxHostlistBytes {
		return "", fmt.Errorf("hostlist exceeds %d bytes", int64(maxHostlistBytes))
	}
	// NUL в списке доменов — признак битой выгрузки; пишем его в файл
	// только чтобы обрезать дальше любой режущий инструмент.
	if strings.ContainsRune(content, 0) {
		return "", fmt.Errorf("hostlist contains a NUL byte")
	}
	// Trailing newline обязателен: без него zapret не увидит последнюю
	// строку (и get_user.sh её тоже).
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	resolved, gz := resolveHostlistFile(l.ipset, spec.filename)
	if gz {
		return "", fmt.Errorf("%s is compressed as .gz and cannot be edited", spec.filename)
	}
	if err := checkWriteTarget(l, resolved); err != nil {
		return "", err
	}
	if err := checkRevision(resolved, revision); err != nil {
		return "", err
	}
	if err := s.checkSelected(l); err != nil {
		return "", err
	}
	mode := fileMode(resolved)
	if err := writeAtomic(resolved, []byte(content), mode); err != nil {
		return "", err
	}
	invalidateHostlistCountCache(resolved)
	// Файл не существует — создаём с обычными правами, но только если
	// это наш каталог ipset (иначе мы бы писали куда попало).
	if _, err := os.Stat(resolved); err != nil {
		return resolved, fmt.Errorf("could not create hostlist %s: %w", spec.filename, err)
	}
	return resolved, nil
}

func invalidateHostlistCountCache(path string) {
	hostlistCountCache.Lock()
	delete(hostlistCountCache.entries, filepath.Clean(path))
	hostlistCountCache.Unlock()
}

func findSpec(id HostlistID) (hostlistSpec, bool) {
	for _, sp := range hostlistSpecs() {
		if sp.id == id {
			return sp, true
		}
	}
	return hostlistSpec{}, false
}
