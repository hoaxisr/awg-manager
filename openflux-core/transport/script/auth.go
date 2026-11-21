package script

import (
	"fmt"
	"net/url"
	"strings"
)

// Auth is what a transport says about the sign-in it needs: which service,
// where a person signs in, which cookies show that the sign-in is there and
// which of them the transport is to be given. The apps build their accounts
// from it (one sign-in per Account, shared by every transport naming it) and
// the core passes the transport only the cookies listed here, so neither has
// to know a service by name.
//
// A script declares it in info().auth (and a package may repeat it in its
// manifest.json under "auth"); Inspect reports it before the script is run.
type Auth struct {
	// Account names the sign-in: transports of one service share it
	// ("yandex" for yandex, vyandex, boards and telemost).
	Account string `json:"account"`
	Label   string `json:"label,omitempty"`
	// Need is when the sign-in is needed: "always" (the transport does not
	// open without it), "create" (only to create the document or room the
	// peers meet in) or "optional" (works without it, better with it).
	Need string `json:"need,omitempty"`

	SignInURL string `json:"signInUrl"`
	// HomeURL is where the service lands a signed-in person; with
	// WaitForHome the sign-in counts only once the page is back there.
	HomeURL     string `json:"homeUrl,omitempty"`
	WaitForHome bool   `json:"waitForHome,omitempty"`
	// FinishURL is opened after the sign-in when the service hands its
	// session over to another site only on a visit (Mail.ru -> Cloud), and
	// FinishCookie is the cookie that shows it got there.
	FinishURL    string `json:"finishUrl,omitempty"`
	FinishCookie string `json:"finishCookie,omitempty"`
	// CookieURLs are the pages whose cookies together make the sign-in.
	CookieURLs []string `json:"cookieUrls,omitempty"`
	// LoginCookie is set only while someone is signed in;
	// LoginNameCookie holds the account's name when the service keeps it in
	// a cookie.
	LoginCookie     string `json:"loginCookie,omitempty"`
	LoginNameCookie string `json:"loginNameCookie,omitempty"`
	// Cookies are the names the transport is given. Empty: all of them.
	Cookies []string `json:"cookies,omitempty"`
}

// Auth.Need values.
const (
	AuthAlways   = "always"
	AuthCreate   = "create"
	AuthOptional = "optional"
)

// Check reports what is wrong with the declaration, nil when nothing.
func (a *Auth) Check() error {
	if a == nil {
		return nil
	}
	if strings.TrimSpace(a.Account) == "" {
		return fmt.Errorf("auth.account is empty")
	}
	switch a.Need {
	case "", AuthAlways, AuthCreate, AuthOptional:
	default:
		return fmt.Errorf("auth.need %q: always, create or optional", a.Need)
	}
	for _, u := range append([]string{a.SignInURL, a.HomeURL, a.FinishURL}, a.CookieURLs...) {
		if u == "" {
			continue
		}
		p, err := url.Parse(u)
		if err != nil || p.Scheme != "https" || p.Host == "" {
			return fmt.Errorf("auth: %q is not an https address", u)
		}
	}
	if a.SignInURL == "" {
		return fmt.Errorf("auth.signInUrl is empty")
	}
	return nil
}

// EffectiveNeed is Need with "absent means always".
func (a *Auth) EffectiveNeed() string {
	if a == nil || a.Need == "" {
		return AuthAlways
	}
	return a.Need
}

// Filter keeps the cookies the transport declared; all of them when it
// declared none (or no auth at all).
func (a *Auth) Filter(values map[string]string) map[string]string {
	if a == nil || len(a.Cookies) == 0 || len(values) == 0 {
		return values
	}
	keep := make(map[string]bool, len(a.Cookies))
	for _, n := range a.Cookies {
		keep[n] = true
	}
	out := make(map[string]string, len(a.Cookies))
	for k, v := range values {
		if keep[k] {
			out[k] = v
		}
	}
	return out
}
