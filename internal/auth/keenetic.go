// Package auth provides authentication via Keenetic RCI API.
package auth

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/sys/netif"
)

const (
	authEndpoint = "/auth"
	httpTimeout  = 10 * time.Second
)

// Швы для тестов: адрес LAN-моста и порт HTTP роутера — хостовые запросы.
var (
	lanIPv4        = netif.FirstIPv4
	routerHTTPPort = getHTTPPort
)

// KeeneticClient handles authentication against Keenetic router.
type KeeneticClient struct {
	routerAddr string
	mu         sync.Mutex
	httpClient *http.Client
}

// NewKeeneticClient creates a new Keenetic auth client.
// Router address (IP + port) is resolved lazily on Authenticate and cached only
// once the LAN address and HTTP port were actually read (see resolveAddr),
// to avoid a race with NDMS at boot — getHTTPPort queries RCI which may
// not be ready when the daemon starts.
func NewKeeneticClient() *KeeneticClient {
	return &KeeneticClient{
		httpClient: &http.Client{
			Timeout: httpTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// resolveAddr returns the router address to use. The result is cached only when
// both the LAN address and the HTTP port were actually read: on early boot RCI
// may not be ready yet, and a guessed "192.168.1.1:80" must not stick until restart.
// Host lookups run outside the mutex so concurrent logins do not queue behind a 5 s RCI timeout.
func (c *KeeneticClient) resolveAddr() string {
	c.mu.Lock()
	cached := c.routerAddr
	c.mu.Unlock()
	if cached != "" {
		return cached
	}
	ip := lanIPv4("br0")
	port, portOK := routerHTTPPort()
	resolved := ip != "" && portOK
	if ip == "" {
		ip = "192.168.1.1"
	}
	addr := ip
	if port != 0 && port != 80 {
		addr = fmt.Sprintf("%s:%d", ip, port)
	}
	if resolved {
		c.mu.Lock()
		if c.routerAddr == "" {
			c.routerAddr = addr
		}
		c.mu.Unlock()
	}
	return addr
}

// getHTTPPort returns router HTTP port from NDMS RCI API. ok is false when the
// running-config could not be read at all (the port is then a default guess).
func getHTTPPort() (port int, ok bool) {
	client := transport.New(transport.NewSemaphore(1))
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var result struct {
		Message []string `json:"message"`
	}
	if err := client.Get(ctx, "/show/running-config", &result); err != nil {
		return 80, false
	}

	for _, line := range result.Message {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ip http port ") {
			var p int
			if _, err := fmt.Sscanf(line, "ip http port %d", &p); err == nil {
				return p, true
			}
		}
	}

	return 80, true
}

// Authenticate verifies credentials against Keenetic router.
// Returns nil on success, error on failure.
func (c *KeeneticClient) Authenticate(ctx context.Context, login, password string) error {
	addr := c.resolveAddr()
	authURL := fmt.Sprintf("http://%s%s", addr, authEndpoint)

	// Step 1: GET /auth to get challenge, realm and cookies
	challenge, realm, cookies, err := c.getChallenge(ctx, authURL)
	if err != nil {
		// If we get 200 on GET, auth is disabled or already authenticated
		if errors.Is(err, errAuthDisabled) {
			return nil
		}
		return fmt.Errorf("get challenge from %s: %w", authURL, err)
	}

	// Step 2: Calculate hashed password
	// Formula: sha256(challenge + md5(login + ":" + realm + ":" + password))
	hashedPassword := c.hashPassword(login, password, realm, challenge)

	// Step 3: POST /auth with credentials (include cookies from GET)
	if err := c.postAuth(ctx, authURL, login, hashedPassword, cookies); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	return nil
}

var errAuthDisabled = fmt.Errorf("auth disabled")

// ErrInvalidCredentials indicates wrong login or password.
var ErrInvalidCredentials = fmt.Errorf("invalid credentials")

// getChallenge performs GET /auth and extracts challenge/realm from 401 response.
// Also returns cookies that must be sent with POST request.
func (c *KeeneticClient) getChallenge(ctx context.Context, authURL string) (challenge, realm string, cookies []*http.Cookie, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL, nil)
	if err != nil {
		return "", "", nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// 200 means auth is disabled or user already authenticated
	if resp.StatusCode == http.StatusOK {
		return "", "", nil, errAuthDisabled
	}

	// 401 is expected - extract challenge and realm
	if resp.StatusCode != http.StatusUnauthorized {
		return "", "", nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	challenge = strings.TrimSpace(resp.Header.Get("X-NDM-Challenge"))
	realm = strings.TrimSpace(resp.Header.Get("X-NDM-Realm"))

	if challenge == "" || realm == "" {
		return "", "", nil, fmt.Errorf("missing challenge or realm headers")
	}

	// Get cookies from response (needed for POST)
	cookies = resp.Cookies()

	return challenge, realm, cookies, nil
}

// hashPassword calculates the Keenetic password hash.
// Formula: sha256(challenge + md5(login + ":" + realm + ":" + password))
func (c *KeeneticClient) hashPassword(login, password, realm, challenge string) string {
	// Step 1: MD5(login:realm:password)
	md5Input := login + ":" + realm + ":" + password
	md5Hash := md5.Sum([]byte(md5Input))
	md5Hex := hex.EncodeToString(md5Hash[:])

	// Step 2: SHA256(challenge + md5hex)
	sha256Input := challenge + md5Hex
	sha256Hash := sha256.Sum256([]byte(sha256Input))
	return hex.EncodeToString(sha256Hash[:])
}

// postAuth sends POST /auth with credentials.
func (c *KeeneticClient) postAuth(ctx context.Context, authURL, login, hashedPassword string, cookies []*http.Cookie) error {
	body := map[string]string{
		"login":    login,
		"password": hashedPassword,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// Add cookies from GET response
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return ErrInvalidCredentials
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected response status: %d", resp.StatusCode)
	}

	return nil
}
