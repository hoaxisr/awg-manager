package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha3"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"net/http"

	"golang.org/x/crypto/argon2"

	"github.com/hoaxisr/awg-manager/internal/sys/appver"
)

// Вход x-ndw4-interactive (KeeneticOS 5.2): SCRAM-подобный обмен на
// Argon2id + HMAC-SHA3-512 в три POST на /auth.
// Протокол восстановлен по веб-интерфейсу 5.2 Alpha 11 и проверен на стенде.
// На 5.2 пароль хранится как ns3, поэтому x-ndw2 с верным паролем отвечает
// 401, хотя роутер его ещё объявляет, — при объявленном ndw4 только он.

var errNDW4NoData = errors.New("401 without " + ndw4DataHeader)

const (
	ndw4ClientKeyLabel = "NDW4 Interactive Client Key"
	ndw4ServerKeyLabel = "NDW4 Interactive Server Key"
	ndw4DataHeader     = "X-NDM-Data"
)

// ndw4Params — ответ фазы 1 (base64 JSON в X-NDM-Data). Memcost — в КиБ,
// как у argon2.IDKey.
type ndw4Params struct {
	Salt    string `json:"salt"`
	Nonce   string `json:"nonce"`
	Iter    uint32 `json:"iter"`
	Memcost uint32 `json:"memcost"`
}

func (c *KeeneticClient) authenticateNDW4(ctx context.Context, url, login, password string, cookies []*http.Cookie) error {
	cnonce := make([]byte, 16)
	rand.Read(cnonce)
	cnonceB64 := base64.StdEncoding.EncodeToString(cnonce)

	var p ndw4Params
	err := c.ndw4Post(ctx, url, cookies, map[string]string{"login": login, "nonce": cnonceB64}, &p)
	// Неизвестный логин — 401 без X-NDM-Data (стенд 5.02.A.11): это неверные
	// креды, а не сбой — иначе 401 против 503 по коду выдавал бы, какие логины
	// есть (по времени разница остаётся: у известного логина ещё Argon2 и
	// фаза 2). На фазе 2 то же — сбой протокола: неверный пароль там приходит
	// подписью.
	if errors.Is(err, errNDW4NoData) {
		return ErrInvalidCredentials
	}
	if err != nil {
		return fmt.Errorf("ndw4 phase 1: %w", err)
	}
	salt, err := base64.StdEncoding.DecodeString(p.Salt)
	if err != nil || len(salt) != 16 || p.Nonce == "" || p.Iter == 0 || p.Memcost == 0 {
		return fmt.Errorf("ndw4 phase 1: malformed params")
	}

	key := argon2.IDKey([]byte(password), salt, p.Iter, p.Memcost, 1, 64)
	clientKey := hmacSHA3(key, ndw4ClientKeyLabel)
	storedKey := sha3.Sum512(clientKey)
	serverKey := hmacSHA3(key, ndw4ServerKeyLabel)
	authMsg := fmt.Sprintf("login1=%s,nonce1=%s;iter2=%d,memcost2=%d,nonce2=%s,salt2=%s;login3=%s,nonce3=%s",
		login, cnonceB64, p.Iter, p.Memcost, p.Nonce, p.Salt, login, p.Nonce)
	proof := xorBytes(clientKey, hmacSHA3(storedKey[:], authMsg))

	var sig struct {
		Signature string `json:"signature"`
	}
	if err := c.ndw4Post(ctx, url, cookies, map[string]string{
		"login": login, "nonce": p.Nonce, "proof": base64.StdEncoding.EncodeToString(proof),
	}, &sig); err != nil {
		return fmt.Errorf("ndw4 phase 2: %w", err)
	}
	// Подпись сервер отдаёт и на неверный пароль — просто не ту (стенд):
	// несовпадение и есть «неверные креды». Фазу 3 тогда не шлём, как и веб-морда.
	got, err := base64.StdEncoding.DecodeString(sig.Signature)
	if err != nil || !hmac.Equal(got, hmacSHA3(serverKey, authMsg)) {
		return ErrInvalidCredentials
	}

	sigProof := xorBytes(clientKey, hmacSHA3(storedKey[:], authMsg+";signature4="+sig.Signature))
	if err := c.ndw4Post(ctx, url, cookies, map[string]string{
		"login": login, "nonce": p.Nonce, "signature-proof": base64.StdEncoding.EncodeToString(sigProof),
	}, nil); err != nil {
		return fmt.Errorf("ndw4 phase 3: %w", err)
	}
	return nil
}

// ndw4Post шлёт фазу обмена. dst != nil — промежуточная фаза: ждём 401 с
// X-NDM-Data; dst == nil — финальная: ждём 200, 401 = неверные креды.
func (c *KeeneticClient) ndw4Post(ctx context.Context, url string, cookies []*http.Cookie, body map[string]string, dst any) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", appver.UA())
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if dst == nil {
		switch resp.StatusCode {
		case http.StatusOK:
			return nil
		case http.StatusUnauthorized:
			return ErrInvalidCredentials
		}
		return fmt.Errorf("unexpected response status: %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("unexpected response status: %d", resp.StatusCode)
	}
	if resp.Header.Get(ndw4DataHeader) == "" {
		return errNDW4NoData
	}
	raw, err := base64.StdEncoding.DecodeString(resp.Header.Get(ndw4DataHeader))
	if err != nil {
		return fmt.Errorf("decode %s: %w", ndw4DataHeader, err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decode %s: %w", ndw4DataHeader, err)
	}
	return nil
}

func hmacSHA3(key []byte, msg string) []byte {
	m := hmac.New(func() hash.Hash { return sha3.New512() }, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}
