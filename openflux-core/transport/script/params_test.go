package script

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func parseParams(t *testing.T, js string) []Param {
	t.Helper()
	var ps []Param
	if err := json.Unmarshal([]byte(js), &ps); err != nil {
		t.Fatalf("params %s: %v", js, err)
	}
	return ps
}

func TestParamDeclarationIsReadLeniently(t *testing.T) {
	ps := parseParams(t, `[
	  {"key":"url","label":"Board","type":"url","required":true},
	  {"key":"retries","label":"Retries","type":"number","default":3,"min":1,"max":10},
	  {"key":"debug","type":"boolean","default":true},
	  {"key":"mode","type":"select","options":["fast",{"value":"safe","label":"Safe"}],"default":"fast"},
	  {"key":"ratio","type":"number","default":0.5}
	]`)
	if ps[1].Default != "3" || *ps[1].Min != 1 || *ps[1].Max != 10 {
		t.Errorf("number: %+v", ps[1])
	}
	if ps[2].Default != "true" {
		t.Errorf("a boolean default is %q, want \"true\"", ps[2].Default)
	}
	if !reflect.DeepEqual(ps[3].Options, []ParamOption{{Value: "fast"}, {Value: "safe", Label: "Safe"}}) {
		t.Errorf("options: %+v", ps[3].Options)
	}
	if ps[4].Default != "0.5" {
		t.Errorf("0.5 -> %q", ps[4].Default)
	}
	// An old script's four fields mean what they always meant.
	if ps[0].Key != "url" || !ps[0].Required || ps[0].Scope != "" || ps[0].Default != "" {
		t.Errorf("old-style param: %+v", ps[0])
	}
	var bad []Param
	if err := json.Unmarshal([]byte(`[{"key":"x","default":{"a":1}}]`), &bad); err == nil {
		t.Error("an object default must be refused")
	}
}

func TestScopes(t *testing.T) {
	scopes := func(js string) []string { return Info{Params: parseParams(t, js)}.scopes() }
	for _, c := range []struct {
		name, js string
		want     []string
	}{
		{"none explicit: first is the profile's", `[{"key":"a"},{"key":"b"},{"key":"c"}]`, []string{"profile", "settings", "settings"}},
		{"only settings declared", `[{"key":"a","scope":"settings"},{"key":"b"}]`, []string{"settings", "settings"}},
		{"explicit profile in the middle", `[{"key":"a"},{"key":"b","scope":"profile"}]`, []string{"settings", "profile"}},
		{"a second profile falls back", `[{"key":"a","scope":"profile"},{"key":"b","scope":"profile"}]`, []string{"profile", "settings"}},
		{"empty", `[]`, []string{}},
	} {
		if got := scopes(c.js); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	info := Info{Params: parseParams(t, `[{"key":"url","type":"url"},{"key":"token","type":"secret"},{"key":"n","type":"number"}]`)}
	if p := info.ProfileParam(); p == nil || p.Key != "url" {
		t.Errorf("ProfileParam = %+v", p)
	}
	if got := info.SettingParams(); len(got) != 2 || got[0].Key != "token" || got[1].Key != "n" {
		t.Errorf("SettingParams = %+v", got)
	}
	if (Info{Params: parseParams(t, `[{"key":"a","scope":"settings"}]`)}).ProfileParam() != nil {
		t.Error("a script with only settings has no profile param")
	}
}

func TestNormalizeSettings(t *testing.T) {
	ps := parseParams(t, `[
	  {"key":"name","type":"text","required":true},
	  {"key":"token","type":"secret"},
	  {"key":"n","type":"number","min":1,"max":10,"default":5},
	  {"key":"on","type":"boolean"},
	  {"key":"mode","type":"select","options":["a","b"],"default":"a"},
	  {"key":"site","type":"url"},
	  {"key":"id","type":"text","pattern":"^[a-z]+$"}
	]`)
	got, errs := NormalizeSettings(ps, map[string]string{"name": "  Bob ", "token": " s p ", "on": "Да", "n": "7", "extra": "dropped"})
	want := map[string]string{"name": "Bob", "token": " s p ", "n": "7", "on": "true", "mode": "a", "site": "", "id": ""}
	if !reflect.DeepEqual(got, want) || len(errs) != 0 {
		t.Errorf("got %v errs %v, want %v", got, errs, want)
	}

	_, errs = NormalizeSettings(ps, map[string]string{"n": "11", "mode": "z", "site": "nope", "id": "ABC"})
	byKey := map[string]string{}
	for _, e := range errs {
		byKey[e.Key] = e.Message
	}
	for _, c := range []struct{ key, has string }{
		{"name", "обязательное"}, {"n", "не больше 10"}, {"mode", "выберите"}, {"site", "https://"}, {"id", "формат"},
	} {
		if !strings.Contains(byKey[c.key], c.has) {
			t.Errorf("%s: %q, want it to mention %q", c.key, byKey[c.key], c.has)
		}
	}
	if _, errs := NormalizeSettings(ps, map[string]string{"name": "x", "n": "abc"}); len(errs) != 1 || errs[0].Key != "n" || !strings.Contains(errs[0].Message, "число") {
		t.Errorf("a non-number: %v", errs)
	}
	if got, _ := NormalizeSettings(ps, map[string]string{"name": "x", "n": "2,5"}); got["n"] != "2,5" {
		t.Errorf("the core does not rewrite a comma decimal (the page does): %q", got["n"])
	}
}

func TestWithDefaultsOnlyFillsWhatIsMissing(t *testing.T) {
	ps := parseParams(t, `[{"key":"a","default":"x"},{"key":"b","default":"y"},{"key":"c"}]`)
	got := WithDefaults(ps, map[string]interface{}{"a": "mine", "b": "", "path": "/p"})
	if got["a"] != "mine" || got["b"] != "y" || got["path"] != "/p" {
		t.Errorf("got %v", got)
	}
	if _, ok := got["c"]; ok {
		t.Error("a param without a default is not invented")
	}
	if got := WithDefaults(ps, nil); got["a"] != "x" {
		t.Errorf("nil values: %v", got)
	}
}

func TestCheckParamsTellsTheAuthor(t *testing.T) {
	ps := parseParams(t, `[
	  {"key":"a","label":"A","type":"text"},
	  {"key":"a","label":"A again","type":"text"},
	  {"key":"b","label":"B","type":"select"},
	  {"key":"c","label":"C","type":"number","min":5,"max":1,"default":9},
	  {"key":"d","type":"widget"},
	  {"key":"e","label":"E","type":"text","pattern":"(["},
	  {"key":"f","label":"F","type":"text","scope":"everywhere"},
	  {"label":"no key","type":"text"}
	]`)
	got := strings.Join(Info{Params: ps}.CheckParams(), "\n")
	for _, want := range []string{
		`"a": the key is declared twice`, `"b": a select needs options`, `"c": min is greater than max`,
		`"d": unknown type "widget"`, `"d": no label`, `"e": pattern does not compile`, `"f": scope must be`, "no key",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CheckParams does not say %q:\n%s", want, got)
		}
	}
	good := parseParams(t, `[{"key":"url","label":"U","type":"url"},{"key":"n","label":"N","type":"number","default":2,"min":1,"max":3}]`)
	if p := (Info{Params: good}).CheckParams(); len(p) != 0 {
		t.Errorf("a clean declaration reported %v", p)
	}
}

func TestSettingsSurviveTheConfLine(t *testing.T) {
	in := map[string]string{
		"token": "a#b;c",
		"notes": "line one\nline two\r\nпривет «мир»",
		"empty": "",
		"pad":   "x=",
	}
	line := EncodeSettings(in)
	if strings.ContainsAny(line, "#;\n\r") {
		t.Fatalf("the line must hold nothing a .conf value is cut at: %q", line)
	}
	out, err := DecodeSettings(line)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range in {
		if out[k] != v {
			t.Errorf("%s: %q, want %q", k, out[k], v)
		}
	}
	for _, bad := range []string{"!!!", EncodeSettings(nil)[:0] + "e30" /* {} is fine */, "bm90LWpzb24"} {
		if _, err := DecodeSettings(bad); (bad == "e30") == (err != nil) {
			t.Errorf("DecodeSettings(%q) err = %v", bad, err)
		}
	}
}

func TestResolvedParamsCarryTheirScope(t *testing.T) {
	info := Info{Params: parseParams(t, `[{"key":"a"},{"key":"b"},{"key":"c","scope":"settings"}]`)}
	got := info.ResolvedParams()
	if got[0].Scope != "profile" || got[1].Scope != "settings" || got[2].Scope != "settings" {
		t.Errorf("scopes = %q %q %q", got[0].Scope, got[1].Scope, got[2].Scope)
	}
	if info.Params[0].Scope != "" {
		t.Error("ResolvedParams must not change the declaration it was made from")
	}
}
