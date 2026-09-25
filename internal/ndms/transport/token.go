package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// Токен доступа к RCI (KeeneticOS 5.2+). С 5.2 ndm на каждый запрос к
// 127.0.0.1:79 без токена пишет W «obsoleted unauthenticated loopback RCI
// access will be removed soon», а снятие обещано. Токен выпускаем сами через
// ndmc (unix-сокет, авторизации не требует): выдаётся на admin, бессрочный,
// переживает ребут (стенд 5.02.A.11, 25.09.2026), но его id после ребута
// перенумеровывается — свои токены находим по описанию, а не по id.
//
// Неверный токен — 403 «0x2312, not identified» и попытка в lockout роутера
// (127.0.0.1 он забанить не может — «invalid address», стенд), поэтому
// перевыпуск не чаще tokenRegenPause. «0x1218, token required» — строка из
// бинаря ndm на случай, когда доступ без токена снимут.
const (
	tokenHeader      = "X-NDMA-TKN"
	tokenDescription = "awg-manager"
	tokenRegenPause  = time.Minute
	tokenNdmcTimeout = 5 * time.Second
)

// tokenRejected — X-Detail отказа именно по токену; прочие 403 не повод
// перевыпускать.
var tokenRejected = []string{"0x2312", "0x1218"}

// errNoTokenCommand — прошивка до 5.2: команды токенов нет, ходим без заголовка.
var errNoTokenCommand = errors.New("ndmc: no authentication token command")

type tokenTransport struct {
	base http.RoundTripper
	ndmc func(cmd string) (string, error)

	mu        sync.Mutex
	path      string // "" — токен не используется (до SetTokenFile)
	token     string
	loaded    bool      // файл уже прочитан
	lastRegen time.Time // последняя попытка выпуска
}

var tokens = &tokenTransport{base: baseTransport, ndmc: runNdmc}

// SetTokenFile задаёт файл токена (в каталоге данных — его делят демон и
// --cleanup). Вызывать до первого запроса к RCI.
func SetTokenFile(path string) {
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	tokens.path, tokens.token, tokens.loaded = path, "", false
}

// RevokeToken удаляет токены awg-manager на роутере и файл — для деинсталляции.
func RevokeToken() error { return tokens.revoke() }

func (t *tokenTransport) revoke() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.path == "" {
		return nil
	}
	// Сначала роутер: не снялся — файл остаётся, и следующая установка
	// найдёт и снимет токен по описанию.
	if err := t.deleteOurs(""); err != nil && !errors.Is(err, errNoTokenCommand) {
		return err
	}
	t.token, t.loaded = "", true
	if err := os.Remove(t.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok := t.current()
	resp, err := t.base.RoundTrip(withToken(req, tok))
	if err != nil || !rejectedToken(resp) {
		return resp, err
	}
	// 403 отбит до исполнения команды, повтор безопасен и для POST — если
	// тело можно переиграть.
	fresh := t.regenerate(tok)
	if fresh == "" || fresh == tok || (req.Body != nil && req.GetBody == nil) {
		return resp, nil
	}
	retry := req.Clone(req.Context())
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return resp, nil
		}
		retry.Body = body
	}
	resp.Body.Close()
	return t.base.RoundTrip(withToken(retry, fresh))
}

func rejectedToken(resp *http.Response) bool {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
		return false
	}
	detail := resp.Header.Get("X-Detail")
	for _, code := range tokenRejected {
		if strings.Contains(detail, code) {
			return true
		}
	}
	return false
}

func withToken(req *http.Request, tok string) *http.Request {
	if tok == "" {
		return req
	}
	r := req.Clone(req.Context())
	r.Header.Set(tokenHeader, tok)
	return r
}

// current отдаёт токен: при первом обращении читает файл, а пока токена
// нет — выпускает (не чаще tokenRegenPause, чтобы сбой выпуска не держал
// процесс без токена до перезапуска).
func (t *tokenTransport) current() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.path == "" || t.token != "" {
		return t.token
	}
	if !t.loaded {
		t.loaded = true
		if b, err := os.ReadFile(t.path); err == nil {
			t.token = strings.TrimSpace(string(b))
		}
		if t.token != "" {
			return t.token
		}
	} else if time.Since(t.lastRegen) < tokenRegenPause {
		return ""
	}
	t.token = t.issue()
	return t.token
}

// regenerate перевыпускает токен после 403 на stale. Уже перевыпущенный
// другим запросом токен отдаётся как есть; чаще tokenRegenPause — "".
func (t *tokenTransport) regenerate(stale string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.path == "" {
		return ""
	}
	if t.token != stale {
		return t.token
	}
	if time.Since(t.lastRegen) < tokenRegenPause {
		return ""
	}
	t.token = t.issue()
	return t.token
}

// issue выпускает новый токен и только потом снимает прежние токены
// awg-manager: упавший выпуск не оставляет роутер без рабочего токена. Под t.mu.
// Отказ — "" (ходим без токена, пока прошивка это терпит).
func (t *tokenTransport) issue() string {
	t.lastRegen = time.Now()
	out, err := t.ndmc("authentication token generate " + tokenDescription)
	if err != nil {
		return ""
	}
	id, tok := parseGeneratedToken(out)
	if tok == "" {
		return ""
	}
	// Не снялись старые — снимутся при следующем выпуске.
	_ = t.deleteOurs(id)
	// Секрет: 0600. Не записался — токен живёт до перезапуска, дальше перевыпуск.
	_ = writeFileAtomic(t.path, []byte(tok+"\n"), 0o600)
	return tok
}

// deleteOurs снимает токены awg-manager, кроме keep.
func (t *tokenTransport) deleteOurs(keep string) error {
	out, err := t.ndmc("show authentication token")
	if err != nil {
		return err
	}
	for _, id := range tokenIDsByDescription(out, tokenDescription) {
		if id == keep {
			continue
		}
		if _, err := t.ndmc("authentication token delete " + id); err != nil {
			return err
		}
	}
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// tokenIDsByDescription разбирает `show authentication token`: блоки
// «id: N … user-data: X». Описание идёт после id внутри блока.
func tokenIDsByDescription(out, desc string) []string {
	var ids []string
	id := ""
	for _, line := range strings.Split(stripANSI(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch k {
		case "id":
			id = strings.TrimSpace(v)
		case "user-data":
			if strings.TrimSpace(v) == desc && id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// parseGeneratedToken достаёт id и значение из вывода generate: «value:» и
// сам токен строкой ниже; ndmc переносит длинные значения по ширине, поэтому
// склеиваем строки-продолжения (без «:») до следующего ключа.
func parseGeneratedToken(out string) (id, tok string) {
	var b strings.Builder
	in := false
	for _, line := range strings.Split(stripANSI(out), "\n") {
		s := strings.TrimSpace(line)
		if k, v, ok := strings.Cut(s, ":"); ok {
			if in {
				break
			}
			switch k {
			case "id":
				id = strings.TrimSpace(v)
			case "value":
				in = true
				b.WriteString(strings.TrimSpace(v))
			}
			continue
		}
		if in {
			if s == "" {
				break
			}
			b.WriteString(s)
		}
	}
	tok = b.String()
	for _, r := range tok {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return "", ""
		}
	}
	if len(tok) < 32 {
		return "", ""
	}
	return id, tok
}

func runNdmc(cmd string) (string, error) {
	res, err := exec.RunWithOptions(context.Background(), "/bin/ndmc", []string{"-c", cmd},
		exec.Options{Timeout: tokenNdmcTimeout})
	// Неизвестная команда: код 127, текст ошибки в stdout (стенд 5.02.A.11).
	if res != nil && strings.Contains(res.Stdout, "no such command") {
		return "", errNoTokenCommand
	}
	if err != nil {
		return "", fmt.Errorf("ndmc %s: %w", cmd, exec.FormatError(res, err))
	}
	return res.Stdout, nil
}

// stripANSI убирает ^[[K, которые печатает ndmc.
func stripANSI(s string) string {
	return strings.ReplaceAll(s, "\x1b[K", "")
}
