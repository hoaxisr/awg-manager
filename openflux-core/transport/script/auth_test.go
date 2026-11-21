package script

import (
	"reflect"
	"testing"
)

const authScript = `var Transport = {
  info: function () {
    return { name: "authdemo", version: "1.0.0", cookieDomain: "https://example.com/",
      auth: { account: "demo", label: "Demo", need: "create", signInUrl: "https://example.com/login",
        homeUrl: "https://example.com/home", cookieUrls: ["https://example.com/"],
        loginCookie: "sid", loginNameCookie: "who", cookies: ["sid", "csrf"] } };
  },
  open: function () {}, send: function () {}, close: function () {}
};`

func TestInspectReportsAuth(t *testing.T) {
	info, err := Inspect([]byte(authScript))
	if err != nil {
		t.Fatal(err)
	}
	want := &Auth{Account: "demo", Label: "Demo", Need: AuthCreate, SignInURL: "https://example.com/login",
		HomeURL: "https://example.com/home", CookieURLs: []string{"https://example.com/"},
		LoginCookie: "sid", LoginNameCookie: "who", Cookies: []string{"sid", "csrf"}}
	if !reflect.DeepEqual(info.Auth, want) {
		t.Fatalf("auth = %+v, want %+v", info.Auth, want)
	}
	r := InspectTrust([]byte(authScript), nil, "", OfficialKeyHex)
	if !r.OK || !reflect.DeepEqual(r.Auth, want) {
		t.Fatalf("report: ok=%v auth=%+v problems=%v", r.OK, r.Auth, r.ParamProblems)
	}
}

func TestAuthCheck(t *testing.T) {
	for name, a := range map[string]*Auth{
		"no account": {SignInURL: "https://x.ru/"},
		"bad need":   {Account: "x", Need: "sometimes", SignInURL: "https://x.ru/"},
		"http":       {Account: "x", SignInURL: "http://x.ru/"},
		"no sign-in": {Account: "x"},
	} {
		if a.Check() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var none *Auth
	if none.Check() != nil || none.EffectiveNeed() != AuthAlways {
		t.Fatal("nil auth")
	}
}

func TestAuthFilter(t *testing.T) {
	a := &Auth{Account: "x", SignInURL: "https://x.ru/", Cookies: []string{"sid"}}
	got := a.Filter(map[string]string{"sid": "1", "tracking": "2"})
	if !reflect.DeepEqual(got, map[string]string{"sid": "1"}) {
		t.Fatalf("filter = %v", got)
	}
	all := map[string]string{"a": "1"}
	if got := (&Auth{Account: "x"}).Filter(all); !reflect.DeepEqual(got, all) {
		t.Fatalf("no list: %v", got)
	}
}
