package transport

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Выводы ndmc сняты со стенда 5.02.A.11.0-1 (25.09.2026), включая ^[[K.
const standGenerateOutput = "\x1b[K\n" + `
               id: 3
            value:
                   1dz8KNQ3YSk74t3W2NtZfmWidUurH8i7XPyXIEOHsHRWmAIdoUWhmSdM

Core::Security::Authenticator: Added a token for user "admin".
` + "\x1b[K"

const standShowOutput = "\x1b[K\n" + `
            token:
                   id: 2
          fingerprint: 27...1f
      truncated-value: kka...3uC
              service: Core::Security::Authenticator
                local: yes
                 user: admin
            user-data: awg-manager
              expires: never
          last-access: 19

            token:
                   id: 3
          fingerprint: 47...06
      truncated-value: 1dz...SdM
              service: Core::Security::Authenticator
                local: yes
                 user: admin
            user-data: xkeen
              expires: never
          last-access: never

            token:
                   id: 4
          fingerprint: 6a...c4
      truncated-value: rRA...xeA
              service: Core::Security::Authenticator
                local: yes
                 user: admin
            user-data: awg-manager
              expires: never
          last-access: never
` + "\x1b[K"

func TestParseGeneratedToken(t *testing.T) {
	cases := []struct{ name, in, id, tok string }{
		{"стенд", standGenerateOutput, "3", "1dz8KNQ3YSk74t3W2NtZfmWidUurH8i7XPyXIEOHsHRWmAIdoUWhmSdM"},
		{"перенос по ширине", "value: \n   1dz8KNQ3YSk74t3W2NtZfmWidUurH8i7\n   XPyXIEOHsHRWmAIdoUWhmSdM\n\nCore::X: y\n", "", "1dz8KNQ3YSk74t3W2NtZfmWidUurH8i7XPyXIEOHsHRWmAIdoUWhmSdM"},
		{"нет value", "id: 3\n", "", ""},
		{"мусор в значении", "value: \n   abc def!ghi-jkl-mno-pqr-stu-vwx-yz0123456789\n", "", ""},
		{"слишком короткий", "value: \n   abc\n", "", ""},
	}
	for _, tc := range cases {
		if id, tok := parseGeneratedToken(tc.in); id != tc.id || tok != tc.tok {
			t.Errorf("%s: %q %q, want %q %q", tc.name, id, tok, tc.id, tc.tok)
		}
	}
}

func TestTokenIDsByDescription(t *testing.T) {
	got := tokenIDsByDescription(standShowOutput, "awg-manager")
	if strings.Join(got, ",") != "2,4" {
		t.Fatalf("ids = %v", got)
	}
	if got := tokenIDsByDescription("\x1b[K\x1b[K", "awg-manager"); len(got) != 0 {
		t.Fatalf("пустой список: %v", got)
	}
}

// fakeNdmc — роутер с токенами из standShowOutput (наши id 2 и 4, чужой 3):
// generate выдаёт следующее значение из gen под id 4.
type fakeNdmc struct {
	mu     sync.Mutex
	cmds   []string
	gen    []string
	genErr bool   // выпуск падает (таймаут ndmc)
	genRaw string // вывод generate как есть, вместо gen
	noop   bool   // прошивка до 5.2
}

func (f *fakeNdmc) run(cmd string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, cmd)
	if f.noop {
		return "", errNoTokenCommand
	}
	switch {
	case cmd == "show authentication token":
		return standShowOutput, nil
	case strings.HasPrefix(cmd, "authentication token generate "):
		if f.genErr {
			return "", errors.New("ndmc: timeout")
		}
		if f.genRaw != "" {
			return f.genRaw, nil
		}
		v := f.gen[0]
		f.gen = f.gen[1:]
		return "id: 4\nvalue: \n   " + v + "\n", nil
	}
	return "", nil
}

func (f *fakeNdmc) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.cmds {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

const (
	tokA = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	tokB = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
)

// rtFunc — base без http.Transport: тот сам переигрывает тело через GetBody,
// и тест не отличил бы наш повтор от его.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// rci — роутер: принимает только токен *valid; иначе 403 с X-Detail detail
// (как на стенде — «0x2312, not identified»). Тела принятых запросов копит.
type rci struct {
	valid, detail string
	allowNone     bool // запрос без токена пускается (5.2 пока так)
	bodies        []string
	seen          []string
}

func (r *rci) RoundTrip(req *http.Request) (*http.Response, error) {
	var b []byte
	if req.Body != nil {
		b, _ = io.ReadAll(req.Body)
	}
	r.seen = append(r.seen, req.Header.Get(tokenHeader))
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: req}
	hdr := req.Header.Get(tokenHeader)
	if r.valid != "" && hdr != r.valid && !(r.allowNone && hdr == "") {
		resp.StatusCode = http.StatusForbidden
		resp.Header.Set("X-Detail", r.detail)
		return resp, nil
	}
	r.bodies = append(r.bodies, string(b))
	return resp, nil
}

func newTestTokens(t *testing.T, f *fakeNdmc, base http.RoundTripper) (*tokenTransport, string) {
	path := filepath.Join(t.TempDir(), "rci-token")
	return &tokenTransport{base: base, ndmc: f.run, path: path, supported: func() bool { return true }}, path
}

func get(t *testing.T, rt http.RoundTripper) int {
	t.Helper()
	resp, err := (&http.Client{Transport: rt}).Get("http://rci.test/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// Первый выпуск: сначала generate, потом снятие прежних своих (2), но не
// только что выпущенного (4) и не чужого (3).
func TestToken_IssuedOnFirstUse(t *testing.T) {
	r := &rci{valid: tokA, detail: "0x2312, not identified"}
	f := &fakeNdmc{gen: []string{tokA}}
	tt, path := newTestTokens(t, f, r)

	for i := 0; i < 3; i++ {
		if code := get(t, tt); code != 200 {
			t.Fatalf("GET %d: %d", i, code)
		}
	}
	if got := strings.Join(f.cmds, "|"); got != "authentication token generate awg-manager|show authentication token|authentication token delete 2" {
		t.Fatalf("ndmc: %s", got)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("файл токена: %v %v", st, err)
	}
	if b, _ := os.ReadFile(path); strings.TrimSpace(string(b)) != tokA {
		t.Fatalf("в файле %q", b)
	}
}

func TestToken_ReadFromFileWithoutNdmc(t *testing.T) {
	f := &fakeNdmc{}
	tt, path := newTestTokens(t, f, &rci{valid: tokA})
	os.WriteFile(path, []byte(tokA+"\n"), 0o600)
	if code := get(t, tt); code != 200 {
		t.Fatalf("GET: %d", code)
	}
	if len(f.cmds) != 0 {
		t.Fatalf("ndmc не нужен при живом файле: %v", f.cmds)
	}
}

// Токен отозван (удалён руками/чужой роутер): 403 → один перевыпуск → повтор
// того же POST с тем же телом. Второй 403 в пределах паузы перевыпуска не
// вызывает.
func TestToken_RegenerateOnceOn403(t *testing.T) {
	r := &rci{valid: tokB, detail: "0x2312, not identified"}
	f := &fakeNdmc{gen: []string{tokB}}
	tt, path := newTestTokens(t, f, r)
	os.WriteFile(path, []byte(tokA), 0o600)
	c := &http.Client{Transport: tt}

	resp, err := c.Post("http://rci.test/", "application/json", bytes.NewBufferString(`[{"x":1}]`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("POST: %v %v", resp, err)
	}
	resp.Body.Close()
	if len(r.bodies) != 1 || r.bodies[0] != `[{"x":1}]` {
		t.Fatalf("повтор с телом: %q", r.bodies)
	}

	r.valid = "другой"
	if code := get(t, tt); code != http.StatusForbidden {
		t.Fatalf("второй 403 обязан вернуться как есть: %d", code)
	}
	if n := f.count("authentication token generate"); n != 1 {
		t.Fatalf("перевыпусков %d, ждали 1", n)
	}
}

// 403 не по токену (другой X-Detail) — не повод перевыпускать.
func TestToken_ForeignForbiddenNoRegen(t *testing.T) {
	r := &rci{valid: "другой", detail: "0x1210, insufficient security level"}
	f := &fakeNdmc{}
	tt, path := newTestTokens(t, f, r)
	os.WriteFile(path, []byte(tokA), 0o600)
	if code := get(t, tt); code != http.StatusForbidden {
		t.Fatalf("GET: %d", code)
	}
	if len(f.cmds) != 0 {
		t.Fatalf("ndmc: %v", f.cmds)
	}
}

// Пока один запрос перевыпускал токен, другой получил 403 на старом: ему
// отдаётся уже выпущенный, второго выпуска нет.
func TestToken_ConcurrentStaleGetsFresh(t *testing.T) {
	f := &fakeNdmc{}
	tt, _ := newTestTokens(t, f, nil)
	tt.token, tt.loaded, tt.lastRegen = tokB, true, time.Now()
	if got := tt.regenerate(tokA); got != tokB {
		t.Fatalf("regenerate = %q", got)
	}
	if len(f.cmds) != 0 {
		t.Fatalf("ndmc: %v", f.cmds)
	}
}

// Упавший выпуск: старые токены на роутере не тронуты, процесс ходит без
// токена, а после паузы выпуск повторяется сам, без 403.
func TestToken_FailedIssueRetriedAfterPause(t *testing.T) {
	r := &rci{}
	f := &fakeNdmc{genErr: true}
	tt, _ := newTestTokens(t, f, r)

	get(t, tt)
	get(t, tt)
	if got := strings.Join(f.cmds, "|"); got != "authentication token generate awg-manager" {
		t.Fatalf("ndmc после отказа: %s", got)
	}
	f.genErr, f.gen = false, []string{tokA}
	tt.mu.Lock()
	tt.lastRegen = time.Now().Add(-tokenRegenPause)
	tt.mu.Unlock()
	get(t, tt)
	if got := strings.Join(r.seen, ","); got != ",,"+tokA {
		t.Fatalf("заголовки: %q", got)
	}
}

// Отзыв: сначала роутер, потом файл; не снялось на роутере — файл остаётся.
func TestToken_Revoke(t *testing.T) {
	f := &fakeNdmc{}
	tt, path := newTestTokens(t, f, nil)
	os.WriteFile(path, []byte(tokA), 0o600)
	if err := tt.revoke(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.cmds, "|"); got != "show authentication token|authentication token delete 2|authentication token delete 4" {
		t.Fatalf("ndmc: %s", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("файл остался: %v", err)
	}

	failing := &tokenTransport{path: path, supported: func() bool { return true }, ndmc: func(string) (string, error) { return "", errors.New("ndm busy") }}
	os.WriteFile(path, []byte(tokA), 0o600)
	if err := failing.revoke(); err == nil {
		t.Fatal("ошибка ndmc проглочена")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("файл снят при несостоявшемся отзыве: %v", err)
	}
}

// Без файла токена (до SetTokenFile, после отзыва) — ни ndmc, ни заголовка.
func TestToken_Unset(t *testing.T) {
	r := &rci{}
	f := &fakeNdmc{}
	unset := &tokenTransport{base: r, ndmc: f.run, supported: func() bool { return true }}
	get(t, unset)
	if r.seen[0] != "" || len(f.cmds) != 0 {
		t.Fatalf("заголовок %q, ndmc %v", r.seen[0], f.cmds)
	}
}

// Без id в выводе generate не снимаем ничего: deleteOurs("") снял бы и только
// что выпущенный токен.
func TestToken_NoIDNoDelete(t *testing.T) {
	f := &fakeNdmc{genRaw: "value: \n   " + tokA + "\n"}
	tt, _ := newTestTokens(t, f, &rci{valid: tokA})
	if code := get(t, tt); code != 200 {
		t.Fatalf("GET: %d", code)
	}
	if got := strings.Join(f.cmds, "|"); got != "authentication token generate awg-manager" {
		t.Fatalf("ndmc: %s", got)
	}
}

// Выпущен, но не разобран — снимаем свои, иначе каждый повтор оставлял бы
// бессрочный admin-токен.
func TestToken_UnparsedIssueCleansUp(t *testing.T) {
	f := &fakeNdmc{genRaw: "id: 9\nvalue: \n   short\n"}
	tt, _ := newTestTokens(t, f, &rci{})
	get(t, tt)
	if got := strings.Join(f.cmds, "|"); got != "authentication token generate awg-manager|show authentication token|authentication token delete 2|authentication token delete 4" {
		t.Fatalf("ndmc: %s", got)
	}
}

// В паузе перевыпуска мёртвый токен забывается, запрос повторяется без
// заголовка и проходит (5.2 пока пускает без токена).
func TestToken_DeadTokenInPauseFallsBackToNone(t *testing.T) {
	r := &rci{valid: tokB, detail: "0x2312, not identified", allowNone: true}
	f := &fakeNdmc{}
	tt, path := newTestTokens(t, f, r)
	os.WriteFile(path, []byte(tokA), 0o600)
	tt.lastRegen = time.Now()
	if code := get(t, tt); code != 200 {
		t.Fatalf("GET: %d", code)
	}
	// Следующий запрос уже без мёртвого токена, без лишнего 403.
	get(t, tt)
	if got := strings.Join(r.seen, ","); got != tokA+",," {
		t.Fatalf("заголовки: %q", got)
	}
	if len(f.cmds) != 0 {
		t.Fatalf("в паузе ndmc не зовётся: %v", f.cmds)
	}
}

// После отзыва запросы токен заново не выпускают.
func TestToken_RevokeIsFinal(t *testing.T) {
	r := &rci{}
	f := &fakeNdmc{gen: []string{tokA}}
	tt, _ := newTestTokens(t, f, r)
	if err := tt.revoke(); err != nil {
		t.Fatal(err)
	}
	n := len(f.cmds)
	get(t, tt)
	if len(f.cmds) != n || r.seen[0] != "" {
		t.Fatalf("после отзыва: ndmc %v, заголовок %q", f.cmds[n:], r.seen[0])
	}
}

// До 5.02 токенов нет: ни ndmc, ни заголовка, ни отзыва — пока версия
// неизвестна, тоже. Узнали 5.02+ — выпуск на следующем запросе.
func TestToken_GatedByFirmware(t *testing.T) {
	r := &rci{}
	f := &fakeNdmc{gen: []string{tokA}}
	tt, _ := newTestTokens(t, f, r)
	is52 := false
	tt.supported = func() bool { return is52 }

	get(t, tt)
	if len(f.cmds) != 0 || r.seen[0] != "" {
		t.Fatalf("до 5.02: ndmc %v, заголовок %q", f.cmds, r.seen[0])
	}
	is52 = true
	get(t, tt)
	if r.seen[1] != tokA {
		t.Fatalf("на 5.02+ заголовок %q", r.seen[1])
	}
}

// Отзыв гейт не смотрит: при неизвестной версии (все каналы молчат) отказ
// оставил бы на роутере бессрочный admin-токен.
func TestToken_RevokeIgnoresGate(t *testing.T) {
	f := &fakeNdmc{}
	tt, _ := newTestTokens(t, f, nil)
	tt.supported = func() bool { return false }
	if err := tt.revoke(); err != nil {
		t.Fatal(err)
	}
	if f.count("authentication token delete") != 2 {
		t.Fatalf("ndmc: %v", f.cmds)
	}
}

// На 4.x/5.01 отзыв (он гейт не смотрит) получает «no such command» — это не
// ошибка: файл снят, отзыв окончательный.
func TestToken_RevokeToleratesNoCommand(t *testing.T) {
	f := &fakeNdmc{noop: true}
	tt, path := newTestTokens(t, f, nil)
	os.WriteFile(path, []byte(tokA), 0o600)
	if err := tt.revoke(); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) || tt.path != "" {
		t.Fatalf("файл %v, path %q", err, tt.path)
	}
}

// SetTokenFile обязан сохранить гейт: без него enabled() ложен всегда, и на
// 5.2 токенов не было бы вовсе.
func TestSetTokenFile_KeepsGate(t *testing.T) {
	saved := tokens
	tokens = &tokenTransport{}
	t.Cleanup(func() { tokens = saved })
	SetTokenFile(filepath.Join(t.TempDir(), "rci-token"), func() bool { return true })
	if !tokens.enabled() {
		t.Fatal("гейт потерян")
	}
}
