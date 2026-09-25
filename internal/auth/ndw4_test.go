package auth

import (
	"bytes"
	"context"
	"crypto/sha3"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"golang.org/x/crypto/argon2"
)

// Сервер ниже — СЕРВЕРНАЯ сторона обмена: он не повторяет вычисление proof
// клиента, а проверяет его как SCRAM-сервер — снимает маску через storedKey
// и сверяет sha3(clientKey) со storedKey. Форма обмена (поля, заголовки,
// метки ключей, строка authMessage) снята с веб-интерфейса 5.2 Alpha 11, а
// сам протокол проверен на стенде прототипом с настоящим паролем.
type ndw4FakeServer struct {
	t                    *testing.T
	storedKey, serverKey []byte
	salt, snonce         string
	cnonce               string
	phases               []string
}

func newNDW4FakeServer(t *testing.T, password string) *ndw4FakeServer {
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte(password), salt, 2, 8, 1, 64)
	ck := hmacSHA3(key, "NDW4 Interactive Client Key")
	sk := sha3.Sum512(ck)
	return &ndw4FakeServer{
		t: t, storedKey: sk[:], serverKey: hmacSHA3(key, "NDW4 Interactive Server Key"),
		salt: base64.StdEncoding.EncodeToString(salt), snonce: "c25vbmNl@abc%def",
	}
}

func (s *ndw4FakeServer) authMsg() string {
	return "login1=admin,nonce1=" + s.cnonce + ";iter2=2,memcost2=8,nonce2=" + s.snonce + ",salt2=" + s.salt + ";login3=admin,nonce3=" + s.snonce
}

// unmask снимает маску proof и проверяет, что клиент знает clientKey.
func (s *ndw4FakeServer) unmask(proofB64, msg string) bool {
	proof, _ := base64.StdEncoding.DecodeString(proofB64)
	ck := xorBytes(proof, hmacSHA3(s.storedKey, msg))
	sum := sha3.Sum512(ck)
	return len(proof) == 64 && bytes.Equal(sum[:], s.storedKey)
}

func (s *ndw4FakeServer) data(w http.ResponseWriter, v any) {
	j, _ := json.Marshal(v)
	w.Header().Set("X-NDM-Data", base64.StdEncoding.EncodeToString(j))
	w.WriteHeader(http.StatusUnauthorized)
}

func (s *ndw4FakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.phases = append(s.phases, "GET")
		w.Header().Add("WWW-Authenticate", `x-ndw2-interactive realm="Keenetic" challenge="c0ffee"`)
		w.Header().Add("WWW-Authenticate", `x-ndw4-interactive endpoint="/auth" data="e30="`)
		w.Header().Set("X-NDM-Challenge", "c0ffee")
		w.Header().Set("X-NDM-Realm", "Keenetic")
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "abc"})
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if ck, err := r.Cookie("sid"); err != nil || ck.Value != "abc" {
		s.t.Errorf("POST без cookie из GET")
	}
	var b map[string]string
	json.NewDecoder(r.Body).Decode(&b)
	switch {
	case b["password"] != "":
		s.phases = append(s.phases, "NDW2")
		w.WriteHeader(http.StatusUnauthorized)
	case b["proof"] != "":
		s.phases = append(s.phases, "P2")
		// Подпись отдаётся и на неверный proof (так на стенде), но не та —
		// клиент обязан сам понять это по несовпадению.
		key := s.serverKey
		if b["nonce"] != s.snonce || !s.unmask(b["proof"], s.authMsg()) {
			key = []byte("not the server key")
		}
		s.data(w, map[string]string{"signature": base64.StdEncoding.EncodeToString(hmacSHA3(key, s.authMsg()))})
	case b["signature-proof"] != "":
		s.phases = append(s.phases, "P3")
		sig := base64.StdEncoding.EncodeToString(hmacSHA3(s.serverKey, s.authMsg()))
		if b["nonce"] == s.snonce && s.unmask(b["signature-proof"], s.authMsg()+";signature4="+sig) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	default:
		s.phases = append(s.phases, "P1")
		if b["login"] != "admin" {
			// Неизвестный логин: 401 без X-NDM-Data (стенд 5.02.A.11).
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.cnonce = b["nonce"]
		if raw, err := base64.StdEncoding.DecodeString(s.cnonce); err != nil || len(raw) != 16 {
			s.t.Errorf("фаза 1: тело %v", b)
		}
		s.data(w, map[string]any{"salt": s.salt, "nonce": s.snonce, "iter": 2, "memcost": 8})
	}
}

func TestAuthenticate_NDW4(t *testing.T) {
	srv := newNDW4FakeServer(t, "s3cret-pw")
	c := newAuthClient(t, srv.ServeHTTP)
	if err := c.Authenticate(context.Background(), "admin", "s3cret-pw"); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got := fmt.Sprint(srv.phases); got != "[GET P1 P2 P3]" {
		t.Fatalf("фазы %s", got)
	}
}

// Неверный пароль: подпись сервера не сходится → ErrInvalidCredentials без
// фазы 3 и без отката на x-ndw2 (на 5.2 он с любым паролем 401 и лишь
// добавил бы неудачу в счётчик lockout роутера).
func TestAuthenticate_NDW4WrongPassword(t *testing.T) {
	srv := newNDW4FakeServer(t, "s3cret-pw")
	c := newAuthClient(t, srv.ServeHTTP)
	if err := c.Authenticate(context.Background(), "admin", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v", err)
	}
	if got := fmt.Sprint(srv.phases); got != "[GET P1 P2]" {
		t.Fatalf("фазы %s", got)
	}
}

// Схема распознаётся по первому слову значения WWW-Authenticate без учёта
// регистра; голое значение без параметров — тоже предложение ndw4.
func TestGetChallenge_NDW4Offer(t *testing.T) {
	cases := []struct {
		hdr  []string
		ndw4 bool
	}{
		{[]string{`x-ndw2-interactive realm="K"`, `x-ndw4-interactive endpoint="/auth" data="e30="`}, true},
		{[]string{`X-NDW4-Interactive`}, true},
		{[]string{`x-ndw2-interactive realm="K"`}, false},
		{[]string{`x-ndw4-interactive-next endpoint="/auth"`}, false},
	}
	for _, tc := range cases {
		c := newAuthClient(t, func(w http.ResponseWriter, r *http.Request) {
			for _, h := range tc.hdr {
				w.Header().Add("WWW-Authenticate", h)
			}
			w.WriteHeader(http.StatusUnauthorized)
		})
		_, _, ndw4, _, err := c.getChallenge(context.Background(), "http://"+c.routerAddr+"/auth")
		if err != nil || ndw4 != tc.ndw4 {
			t.Errorf("%q: ndw4=%v err=%v, want %v", tc.hdr, ndw4, err, tc.ndw4)
		}
	}
}

// Неизвестный логин — неверные креды, а не сбой роутера: иначе API отвечал бы
// 503 вместо 401 и выдавал, какие логины существуют.
func TestAuthenticate_NDW4UnknownLogin(t *testing.T) {
	srv := newNDW4FakeServer(t, "s3cret-pw")
	c := newAuthClient(t, srv.ServeHTTP)
	if err := c.Authenticate(context.Background(), "nosuch", "x"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v", err)
	}
}

// 401 без данных на фазе 2 — сбой протокола, а не неверный пароль (тот
// приходит подписью): иначе поломка протокола засчитывалась бы в троттл
// входа и блокировала пользователя с верным паролем.
func TestAuthenticate_NDW4Phase2NoDataIsNotCredentials(t *testing.T) {
	srv := newNDW4FakeServer(t, "s3cret-pw")
	c := newAuthClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && len(srv.phases) == 2 {
			srv.phases = append(srv.phases, "P2")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		srv.ServeHTTP(w, r)
	})
	err := c.Authenticate(context.Background(), "admin", "s3cret-pw")
	if err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v", err)
	}
}
