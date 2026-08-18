package subscription

import (
	"net/url"
	"strings"
)

// NormalizeSubscriptionURL strips wrapper schemes (happ://, clash://, etc.)
// and ensures a clean http/https URL. Returns normalized URL and whether a change occurred.
func NormalizeSubscriptionURL(rawURL string) (string, bool) {
	trimmed := strings.TrimSpace(rawURL)
	lower := strings.ToLower(trimmed)

	// happ://crypt/, happ://crypt2/, happ://crypt3/, happ://crypt4/
	if IsHappCryptLink(trimmed) {
		if decrypted, err := DecryptHappLink(trimmed); err == nil && decrypted != "" {
			return decrypted, true
		}
		return trimmed, false
	}

	// clash://install-config?url=... or clashmeta://install-config?url=...
	if strings.HasPrefix(lower, "clash://install-config") || strings.HasPrefix(lower, "clashmeta://install-config") {
		if u, err := url.Parse(trimmed); err == nil {
			if target := u.Query().Get("url"); target != "" {
				return target, true
			}
		}
	}

	// Any wrapper scheme containing https:// or http:// (e.g. happ://add/https://..., sub://https://...)
	for _, scheme := range []string{"happ://", "sub://", "singbox://", "sing-box://", "v2ray://", "sn://"} {
		if strings.HasPrefix(lower, scheme) {
			after := trimmed[len(scheme):]
			lowerAfter := lower[len(scheme):]
			if idx := strings.Index(lowerAfter, "https://"); idx != -1 {
				return after[idx:], true
			}
			if idx := strings.Index(lowerAfter, "http://"); idx != -1 {
				return after[idx:], true
			}
			if scheme == "happ://" {
				// happ://domain.com/path... -> https://domain.com/path...
				return "https://" + after, true
			}
		}
	}

	return trimmed, false
}

// RewriteForRaw maps a small set of well-known git-hosting web-view
// ("blob") URLs and proxy client schemas (happ://, clash://) to the
// equivalent raw-content URL. Returns the rewritten URL plus a boolean
// indicating whether a rewrite happened.
func RewriteForRaw(rawURL string) (string, bool) {
	normalized, normRewrote := NormalizeSubscriptionURL(rawURL)
	if normRewrote {
		rawURL = normalized
	}

	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return rawURL, normRewrote
	}
	host := strings.ToLower(u.Host)

	// Web landing page handoff (e.g. links.clovpn.org/happ?id=... -> /api/sub?id=...)
	if (u.Path == "/happ" || strings.HasSuffix(u.Path, "/happ")) && (u.Query().Get("id") != "" || u.Query().Get("token") != "") {
		u.Path = strings.TrimSuffix(u.Path, "/happ") + "/api/sub"
		return u.String(), true
	}

	// github.com/{owner}/{repo}/blob/{ref}/{path...}
	if host == "github.com" {
		parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 5)
		if len(parts) >= 5 && parts[2] == "blob" {
			u.Host = "raw.githubusercontent.com"
			u.Path = "/" + parts[0] + "/" + parts[1] + "/" + parts[3] + "/" + parts[4]
			return u.String(), true
		}
	}

	// GitLab — same host, /-/blob/ → /-/raw/. Covers gitlab.com and any
	// self-hosted instance using the standard layout.
	if i := strings.Index(u.Path, "/-/blob/"); i != -1 {
		u.Path = u.Path[:i] + "/-/raw/" + u.Path[i+len("/-/blob/"):]
		return u.String(), true
	}

	// Gitea / Forgejo — same host, /src/branch/ → /raw/branch/ (and
	// /src/commit/ → /raw/commit/ , /src/tag/ → /raw/tag/).
	for _, ref := range []string{"branch", "commit", "tag"} {
		from := "/src/" + ref + "/"
		to := "/raw/" + ref + "/"
		if i := strings.Index(u.Path, from); i != -1 {
			u.Path = u.Path[:i] + to + u.Path[i+len(from):]
			return u.String(), true
		}
	}

	return rawURL, normRewrote
}
