package phphost

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// browserUA is what the site sees: several free hosts turn away anything that
// does not look like a browser.
const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36"

// Site is a deployed node: where it is, its token, and how to carry the
// requests. Zero Client uses a fresh one (with a cookie jar, for the host's
// browser check).
type Site struct {
	URL     string // the site's address: https://example.42web.io
	Token   string
	Carrier string // "cupsonline" or "mailru": which exit file to talk to
	Client  *http.Client

	mu        sync.Mutex
	ownClient bool                                                                  // Client was made here (not handed in), so it may be paired with pinned ones
	pin       string                                                                // the address that last answered as the node: asked first from then on
	ips       map[string][]string                                                   // host -> candidate addresses (resolve.go)
	dialHook  func(ctx context.Context, network, ip, port string) (net.Conn, error) // tests only
}

func exitFile(carrier string) string {
	if carrier == "mailru" {
		return "mailruexit.php"
	}
	return "cupsexit.php"
}

func (s *Site) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	jar, _ := cookiejar.New(nil)
	// Free and cheap hosts often serve a generic certificate on a shared IP (one that is valid, just not for this
	// account's own domain), or a self-signed one on a custom domain the user pointed here: the node behind it can
	// be perfectly fine. Checking the certificate would refuse a working site over a paperwork mismatch we cannot
	// fix anyway - the token in the URL is what actually proves this is the user's own node, not the TLS chain.
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: s.dialContext}
	s.Client = &http.Client{Jar: jar, Timeout: 30 * time.Second, Transport: tr}
	s.ownClient = true
	return s.Client
}

// pinnedClient is client() but always connecting to ip, sharing its cookies (the host's browser check is per
// address too, so an answer from another address may need it solved there).
func (s *Site) pinnedClient(ip string) *http.Client {
	base := s.client()
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: s.dialFixed(ip)}
	return &http.Client{Jar: base.Jar, Timeout: 15 * time.Second, Transport: tr}
}

func (s *Site) endpoint(q url.Values) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(s.URL), "/"))
	if err != nil || u.Host == "" {
		return "", fail(CodeBadParams, "url", "phphost: the site address is not a URL", err)
	}
	if u.Scheme == "" {
		u.Scheme = "https"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + exitFile(s.Carrier)
	q.Set("k", s.Token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// PageURL is the node's control panel for a person's browser: its status, log, Start/Stop and the generation
// serving now. auto=0: opening it only looks; it does not start a node that is not running.
func (s *Site) PageURL(target string) (string, error) {
	return s.endpoint(url.Values{"url": {strings.TrimSpace(target)}, "auto": {"0"}})
}

// The browser check some free hosts (InfinityFree and other iFastNet sites) put in
// front of every page: a script decrypts a value with AES-128-CBC and sets it as the
// cookie "__test", then reloads with ?i=1. A browser does that unseen; so can we.
var (
	antiBotNums   = regexp.MustCompile(`toNumbers\("([0-9a-fA-F]+)"\)`)
	antiBotMarker = regexp.MustCompile(`slowAES|aes\.js`)
)

// solveAntiBot returns the cookie the page asks for, if it is that check.
func solveAntiBot(body []byte) (name, value string, ok bool) {
	if !antiBotMarker.Match(body) {
		return "", "", false
	}
	m := antiBotNums.FindAllSubmatch(body, -1)
	if len(m) < 3 {
		return "", "", false
	}
	key, e1 := hex.DecodeString(string(m[0][1]))
	iv, e2 := hex.DecodeString(string(m[1][1]))
	ct, e3 := hex.DecodeString(string(m[2][1]))
	if e1 != nil || e2 != nil || e3 != nil || len(key) != 16 || len(iv) != 16 || len(ct) == 0 || len(ct)%16 != 0 {
		return "", "", false
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return "", "", false
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(blk, iv).CryptBlocks(pt, ct)
	return "__test", hex.EncodeToString(pt), true
}

// challengePage reports a page that is some browser check (this one, or another we cannot pass).
func challengePage(body []byte) bool {
	l := strings.ToLower(string(body[:min(len(body), 4096)]))
	return antiBotMarker.MatchString(l) || strings.Contains(l, "just a moment") || strings.Contains(l, "cf-browser-verification") ||
		strings.Contains(l, "captcha") || strings.Contains(l, "enable javascript")
}

// get fetches one URL, passing the host's browser check when it can.
func (s *Site) get(ctx context.Context, raw string) (int, []byte, error) {
	return s.getWith(ctx, s.client(), raw)
}

// getWith is get over a given client.
func (s *Site) getWith(ctx context.Context, cl *http.Client, raw string) (int, []byte, error) {
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return 0, nil, fail(CodeBadParams, "url", "phphost: bad request", err)
		}
		req.Header.Set("User-Agent", browserUA)
		req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")
		resp, err := cl.Do(req)
		if err != nil {
			return 0, nil, fail(CodeSiteUnreachable, req.URL.Host, "phphost: the site did not answer", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if name, val, ok := solveAntiBot(body); ok {
			u, _ := url.Parse(raw)
			cl.Jar.SetCookies(u, []*http.Cookie{{Name: name, Value: val, Path: "/"}})
			q := u.Query()
			q.Set("i", "1") // what the page's own script reloads with
			u.RawQuery = q.Encode()
			raw = u.String()
			continue
		}
		return resp.StatusCode, body, nil
	}
	return 0, nil, fail(CodeAntiBot, "", "phphost: the host's browser check did not let the request through", nil)
}

// altScheme is raw with http<->https swapped.
func altScheme(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "https" {
		u.Scheme = "http"
	} else {
		u.Scheme = "https"
	}
	return u.String(), nil
}

// ours reports whether an answer came from a phpbox node at all: its own refusal ("no", when the token is wrong) or
// a JSON object. Anything else - a host's 404 page, some other site, a parking page - is someone else answering.
func ours(code int, body []byte) bool {
	t := bytes.TrimSpace(body)
	return string(t) == "no" || (len(t) > 0 && t[0] == '{' && json.Valid(t))
}

// getValid is get, but it does not take the first answer at face value. If the answer is not the node's (see ours),
// or the connection fails, it goes on: the other scheme (a free host is often plain HTTP only, or the user typed the
// wrong one), and every other address the site resolves to (resolve.go: a device's DNS can point at a server that
// is not the site's). The first answer that is the node's wins, and its address is remembered for this Site's later
// requests; when none is, the first answer given is returned as it was, so the caller can say what it saw.
// Not used for a=run: that request IS the node, and sending it to several addresses would start several.
func (s *Site) getValid(ctx context.Context, raw string) (int, []byte, error) {
	var (
		haveResp  bool
		firstCode int
		firstBody []byte
		errs      []string
		firstErr  error
	)
	note := func(code int, body []byte, err error) {
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			errs = append(errs, err.Error())
			return
		}
		if !haveResp {
			haveResp, firstCode, firstBody = true, code, body
		}
	}
	urls := []string{raw}
	if alt, err := altScheme(raw); err == nil {
		urls = append(urls, alt)
	}
	for _, u := range urls {
		code, body, err := s.getWith(ctx, s.client(), u)
		if err == nil && ours(code, body) {
			return code, body, nil
		}
		note(code, body, err)
		if !s.ownClient { // a client handed in is used as it is
			continue
		}
		pu, perr := url.Parse(u)
		if perr != nil {
			continue
		}
		ips := s.ordered(ctx, pu.Hostname())
		for i, ip := range ips {
			if i == 0 {
				continue // the default client has just asked this one first
			}
			code, body, err := s.getWith(ctx, s.pinnedClient(ip), u)
			if err == nil && ours(code, body) {
				s.setPin(ip)
				return code, body, nil
			}
			note(code, body, err)
		}
	}
	if haveResp {
		return firstCode, firstBody, nil
	}
	if len(errs) > 1 {
		host := ""
		if pu, err := url.Parse(raw); err == nil {
			host = pu.Host
		}
		return 0, nil, fail(CodeSiteUnreachable, host, fmt.Sprintf("phphost: the site did not answer on any scheme or address tried (%s)", strings.Join(errs, "; ")), nil)
	}
	return 0, nil, firstErr
}

// snippet is body, trimmed and flattened to one line, for a log a person can actually read.
func snippet(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")
	const max = 200
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// Status is what the node's ping says about the host.
type Status struct {
	Version  string          `json:"phpbox"`
	Carrier  string          `json:"carrier"`
	PHP      string          `json:"php"`
	Missing  []string        `json:"missing"`
	StateDir bool            `json:"state_dir"`
	Parser   bool            `json:"parser"`
	Needs    map[string]bool `json:"needs,omitempty"`
}

// Check asks the site whether the node is there and whether the host can run it.
func (s *Site) Check(ctx context.Context) (*Status, error) {
	raw, err := s.endpoint(url.Values{"a": {"ping"}})
	if err != nil {
		return nil, err
	}
	code, body, err := s.getValid(ctx, raw)
	if err != nil {
		return nil, err
	}
	// Every action needs the right token first (lib/node.php checks it before looking at "a"), and refuses with
	// exactly this - whatever the status code a host's own front end wraps it in.
	if strings.TrimSpace(string(body)) == "no" {
		return nil, fail(CodeTokenRefused, "", fmt.Sprintf("phphost: the node refused the token (http %d)", code), nil)
	}
	var st Status
	if code != http.StatusOK || json.Unmarshal(body, &st) != nil || st.Version == "" {
		if challengePage(body) {
			return nil, fail(CodeAntiBot, "", "phphost: a browser check stands in front of the site", nil)
		}
		return nil, fail(CodeNotPhpbox, "",
			fmt.Sprintf("phphost: the site does not answer as a phpbox node (http %d, body: %s)", code, snippet(body)), nil)
	}
	if len(st.Missing) > 0 {
		return &st, fail(CodePHPMissing, strings.Join(st.Missing, ","), "phphost: the host lacks what the node needs", nil)
	}
	return &st, nil
}

// NodeState is the node's own account of itself (a=status).
type NodeState struct {
	Running  bool `json:"running"`
	Draining int  `json:"draining"`
	Chain    bool `json:"chain"`
	NextIn   *int `json:"next_in"`
	Stopping bool `json:"stopping"`
	State    struct {
		Gen    int    `json:"gen"`
		Phase  string `json:"phase"`
		Reason string `json:"reason"`
	} `json:"state"`
}

// Node reports whether the node on target is running.
func (s *Site) Node(ctx context.Context, target string) (*NodeState, error) {
	raw, err := s.endpoint(url.Values{"a": {"status"}, "url": {target}})
	if err != nil {
		return nil, err
	}
	code, body, err := s.getValid(ctx, raw)
	if err != nil {
		return nil, err
	}
	var ns NodeState
	if code != http.StatusOK || json.Unmarshal(body, &ns) != nil {
		return nil, fail(CodeNotPhpbox, "", fmt.Sprintf("phphost: unreadable status (http %d, body: %s)", code, snippet(body)), nil)
	}
	return &ns, nil
}

// StartOptions tune Start.
type StartOptions struct {
	Chain     bool // keep the tunnel up continuously (the self-renewing node)
	Sensitive bool // log destinations on the host
}

// Start asks the site to run the node on target (the room URL, or the
// document link). The request IS the node, which lives on after we hang up, so
// we wait only a moment for it, then ask for its status until it reports
// running. A node already running there is left alone and reported as running.
func (s *Site) Start(ctx context.Context, target string, o StartOptions, wait time.Duration) (*NodeState, error) {
	if ns, err := s.Node(ctx, target); err == nil && ns.Running {
		return ns, nil
	}
	q := url.Values{"a": {"run"}, "url": {target}}
	if o.Chain {
		q.Set("chain", "1")
	}
	if o.Sensitive {
		q.Set("sensitive", "1")
	}
	raw, err := s.endpoint(q)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	_, _, err = s.get(rctx, raw) // the host keeps the node running when we let go
	cancel()
	var e *Error
	if err != nil && errors.As(err, &e) && (e.Code == CodeAntiBot || e.Code == CodeBadParams) {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	var lastErr error
	polls := 0
	for {
		ns, nerr := s.Node(ctx, target)
		if nerr == nil && ns.Running {
			return ns, nil
		}
		polls++
		lastErr = nerr
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, fail(CodeNodeNotStarted, "",
				fmt.Sprintf("phphost: the node did not report running after %d poll(s); last: %v", polls, lastErr), nil)
		}
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-ctx.Done():
			return nil, fail(CodeNodeNotStarted, "", "phphost: cancelled", ctx.Err())
		}
	}
}

// Stop asks the node to end (the whole chain).
func (s *Site) Stop(ctx context.Context, target string) error {
	raw, err := s.endpoint(url.Values{"a": {"stop"}, "url": {target}})
	if err != nil {
		return err
	}
	_, _, err = s.getValid(ctx, raw)
	return err
}
