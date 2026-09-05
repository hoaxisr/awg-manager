package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/netif"
)

// Форма /auth на живом стенде НЕ снималась: тесты пинят контракт кода
// (порядок GET→POST, имена заголовков, форму тела), а не поведение прошивки.

// Известный вектор: md5("admin:Keenetic:s3cret-pw") = 7ada9ea6980d5fc12ae3cfe79df14a84,
// sha256("c0ffee1234" + md5hex) = 02e085441786ba6310bd3ee83d4a05dd0e60c13ce8b2c893f614eb4019852230
// (посчитано stdlib-скриптом вне кода проекта). Порядок аргументов (login, password, realm, challenge)
// НЕ совпадает с порядком конкатенации login:realm:password — перестановка даёт другой хеш
// (e6d100b197a63dcb0b021102b5abfd51304372c0b564a712ce43730366007de5).
func TestHashPassword_KnownVector(t *testing.T) {
	c := &KeeneticClient{}
	if got := c.hashPassword("admin", "s3cret-pw", "Keenetic", "c0ffee1234"); got != "02e085441786ba6310bd3ee83d4a05dd0e60c13ce8b2c893f614eb4019852230" {
		t.Fatalf("hashPassword = %s", got)
	}
}

func newAuthClient(t *testing.T, h http.HandlerFunc) *KeeneticClient {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	c := NewKeeneticClient()
	// адрес задан заранее — resolveAddr не пойдёт в br0/RCI хоста
	c.routerAddr = strings.TrimPrefix(ts.URL, "http://")
	return c
}

// Полный обмен: GET /auth → 401 + challenge/realm + cookie; POST /auth с хешем и cookie → 200.
func TestAuthenticate_ChallengeThenPost(t *testing.T) {
	var gotBody map[string]string
	var gotCookie, gotCT string
	c := newAuthClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth" {
			t.Errorf("path %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("X-NDM-Challenge", " c0ffee1234 ")
			w.Header().Set("X-NDM-Realm", "Keenetic")
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "abc"})
			w.WriteHeader(http.StatusUnauthorized)
		case http.MethodPost:
			gotCT = r.Header.Get("Content-Type")
			if ck, err := r.Cookie("sid"); err == nil {
				gotCookie = ck.Value
			}
			json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusOK)
		}
	})
	if err := c.Authenticate(context.Background(), "admin", "s3cret-pw"); err != nil {
		t.Fatal(err)
	}
	if gotBody["login"] != "admin" || gotBody["password"] != "02e085441786ba6310bd3ee83d4a05dd0e60c13ce8b2c893f614eb4019852230" || gotCookie != "abc" || gotCT != "application/json" {
		t.Fatalf("POST: body=%v cookie=%q ct=%q", gotBody, gotCookie, gotCT)
	}
}

func TestAuthenticate_Outcomes(t *testing.T) {
	cases := []struct {
		name    string
		get     func(w http.ResponseWriter)
		post    int
		wantErr func(error) bool
	}{
		// сравнение — errors.Is, обёртка внутри getChallenge не сломает путь
		{"auth disabled → nil", func(w http.ResponseWriter) { w.WriteHeader(200) }, 0, func(e error) bool { return e == nil }},
		{"unexpected GET status", func(w http.ResponseWriter) { w.WriteHeader(500) }, 0, func(e error) bool {
			return e != nil && strings.Contains(e.Error(), "unexpected status: 500")
		}},
		{"missing headers", func(w http.ResponseWriter) { w.WriteHeader(401) }, 0, func(e error) bool {
			return e != nil && strings.Contains(e.Error(), "missing challenge or realm")
		}},
		{"wrong password", func(w http.ResponseWriter) {
			w.Header().Set("X-NDM-Challenge", "x")
			w.Header().Set("X-NDM-Realm", "r")
			w.WriteHeader(401)
		}, 401, func(e error) bool { return errors.Is(e, ErrInvalidCredentials) }},
		{"post 500", func(w http.ResponseWriter) {
			w.Header().Set("X-NDM-Challenge", "x")
			w.Header().Set("X-NDM-Realm", "r")
			w.WriteHeader(401)
		}, 500, func(e error) bool {
			return e != nil && strings.Contains(e.Error(), "unexpected response status: 500")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newAuthClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					tc.get(w)
					return
				}
				if tc.post == 0 {
					t.Errorf("POST не ожидался в случае %q", tc.name)
					w.WriteHeader(http.StatusTeapot)
					return
				}
				w.WriteHeader(tc.post)
			})
			if err := c.Authenticate(context.Background(), "admin", "pw"); !tc.wantErr(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// Пока RCI не отвечает, догадка «192.168.1.1» не кэшируется: следующий логин снова
// спрашивает br0 и порт. Раньше sync.Once навсегда фиксировал догадку первого вызова.
func TestResolveAddr_FailedPortLookupIsNotCached(t *testing.T) {
	calls := 0
	oldIP, oldPort := lanIPv4, routerHTTPPort
	lanIPv4 = func(string) string { return "" }
	routerHTTPPort = func() (int, bool) { calls++; return 80, false }
	t.Cleanup(func() { lanIPv4, routerHTTPPort = oldIP, oldPort })

	c := NewKeeneticClient()
	if got := c.resolveAddr(); got != "192.168.1.1" {
		t.Fatalf("адрес по догадке = %q", got)
	}
	if got := c.resolveAddr(); got != "192.168.1.1" || calls != 2 {
		t.Fatalf("второй вызов обязан спросить порт снова: calls=%d addr=%q", calls, got)
	}
	// RCI поднялся — с этого момента адрес с портом фиксируется.
	lanIPv4 = func(string) string { return "192.168.5.1" }
	routerHTTPPort = func() (int, bool) { calls++; return 8080, true }
	if got := c.resolveAddr(); got != "192.168.5.1:8080" {
		t.Fatalf("после успешного разрешения = %q", got)
	}
	if got := c.resolveAddr(); got != "192.168.5.1:8080" || calls != 3 {
		t.Fatalf("успешное разрешение обязано кэшироваться: calls=%d addr=%q", calls, got)
	}
}

// Порт 80 из прочитанного running-config — тоже успех: адрес без суффикса порта и кэш взведён.
func TestResolveAddr_Port80FromConfigIsCached(t *testing.T) {
	calls := 0
	oldIP, oldPort := lanIPv4, routerHTTPPort
	lanIPv4 = func(string) string { return "192.168.5.1" }
	routerHTTPPort = func() (int, bool) { calls++; return 80, true }
	t.Cleanup(func() { lanIPv4, routerHTTPPort = oldIP, oldPort })
	c := NewKeeneticClient()
	if got := c.resolveAddr(); got != "192.168.5.1" {
		t.Fatalf("адрес = %q", got)
	}
	c.resolveAddr()
	if calls != 1 {
		t.Fatalf("порт спрошен %d раз, ждали 1", calls)
	}
}

// Дефолты швов — прод-функции.
func TestResolveAddr_SeamDefaults(t *testing.T) {
	if reflect.ValueOf(lanIPv4).Pointer() != reflect.ValueOf(netif.FirstIPv4).Pointer() {
		t.Fatal("lanIPv4 != netif.FirstIPv4")
	}
	if reflect.ValueOf(routerHTTPPort).Pointer() != reflect.ValueOf(getHTTPPort).Pointer() {
		t.Fatal("routerHTTPPort != getHTTPPort")
	}
}
