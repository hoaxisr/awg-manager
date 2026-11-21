package script

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const settingsScript = `
var Transport = {
  info: function () {
    return {
      name: "Demo <b>", version: "1.2.0",
      params: [
        { key: "url", label: "Board URL", type: "url", required: true },
        { key: "token", label: "API token", type: "secret", required: true, description: "From the \"account\" page", group: "Account" },
        { key: "retries", label: "Retries", type: "number", default: 3, min: 1, max: 10, group: "Account" },
        { key: "mode", label: "Mode", type: "select", options: ["fast", { value: "safe", label: "Safe & slow" }], default: "fast" },
        { key: "debug", label: "Debug log", type: "boolean", default: false },
        { key: "notes", label: "Notes", type: "textarea", placeholder: "free text", advanced: true },
        { key: "id", label: "Id", type: "text", pattern: "^[a-z]+$", advanced: true }
      ]
    };
  },
  open: function (cfg) {}, write: function () {}, close: function () {}
};
`

func infoOf(t *testing.T, src string) Info {
	t.Helper()
	info, err := Inspect([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestSettingsPageIsBuiltFromTheDeclaration(t *testing.T) {
	info := infoOf(t, settingsScript)
	page := SettingsPage(info, map[string]string{"token": `tok"en<script>`, "retries": "4", "mode": "safe", "debug": "true"}, PageOptions{})

	for _, want := range []string{
		`<html lang="ru">`, `<h1>Demo &lt;b&gt;</h1>`, `Настройки · v1.2.0`,
		// the profile's own param (the first) also gets a field here: the
		// wizard and the profile editor edit the one saved value.
		`Board URL`,
		`API token`, `Retries`, `Mode`, `Debug log`,
		`<h2 class="group">Account</h2>`,
		`type="password"`, `data-show=`, `inputmode="decimal"`, `<select id=`, `type="checkbox" id=`, `<textarea id=`,
		`<option value="safe" selected>Safe &amp; slow</option>`,
		`<option value="fast">fast</option>`,
		`<details class="adv"><summary>Дополнительно</summary>`,
		`placeholder="free text"`, `From the &#34;account&#34; page`,
		`value="4"`, ` checked>`,
		`Сохранить`, `По умолчанию`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// What a script declares is data: nothing of it may become markup.
	if strings.Contains(page, "<b>") || strings.Contains(page, `tok"en<script>`) {
		t.Error("a label or a value reached the page unescaped")
	}
	if n := strings.Count(page, "<script>"); n != 1 {
		t.Errorf("%d <script> elements, want exactly the page's own", n)
	}
	// the embedded data is valid JSON and cannot close the script element
	i := strings.Index(page, "var DATA=")
	j := strings.Index(page[i:], ";(function(){")
	var data struct {
		Params []Param           `json:"params"`
		Values map[string]string `json:"values"`
	}
	if err := json.Unmarshal([]byte(page[i+len("var DATA="):i+j]), &data); err != nil {
		t.Fatalf("DATA is not JSON: %v", err)
	}
	if len(data.Params) != 7 || data.Values["token"] != `tok"en<script>` {
		t.Errorf("DATA = %+v", data)
	}
	if strings.Contains(page[i:i+j], "<") {
		t.Error("DATA holds a raw '<'")
	}
}

func TestSettingsPageLanguageAndEmpty(t *testing.T) {
	info := infoOf(t, settingsScript)
	en := SettingsPage(info, nil, PageOptions{Lang: "en"})
	if !strings.Contains(en, `<html lang="en">`) || !strings.Contains(en, ">Save<") || !strings.Contains(en, "Advanced") {
		t.Error("the English words are not used")
	}
	if got := SettingsPage(info, nil, PageOptions{Lang: "xx"}); !strings.Contains(got, `lang="ru"`) {
		t.Error("an unknown language falls back to Russian")
	}
	none := SettingsPage(infoOf(t, `var Transport={info:function(){return{name:"x",params:[]}},open:function(){}}`), nil, PageOptions{})
	if !strings.Contains(none, "нет настроек") {
		t.Error("a script with nothing to set says so")
	}
	// defaults show when nothing was saved
	dflt := SettingsPage(info, nil, PageOptions{})
	if !strings.Contains(dflt, `value="3"`) || !strings.Contains(dflt, `<option value="fast" selected>`) {
		t.Error("declared defaults must prefill the form")
	}
}

func signedFile(t *testing.T, src string) (data, sig []byte, pubHex string) {
	t.Helper()
	path, pub := mustSignedScript(t, src)
	data, _ = os.ReadFile(path)
	sig, _ = os.ReadFile(path + ".sig")
	return data, sig, hexOf(pub)
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}

func TestBuildSettings(t *testing.T) {
	data, sig, key := signedFile(t, settingsScript)

	rep := BuildSettings(data, sig, key, map[string]string{"token": "t", "retries": "x"}, "ru")
	if !rep.OK || rep.Signature != "valid" || rep.Name != "Demo <b>" || rep.Custom || rep.HTML == "" {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Params) != 7 || rep.Params[0].Key != "url" {
		t.Errorf("params = %+v", rep.Params)
	}
	if rep.Values["retries"] != "x" || rep.Values["mode"] != "fast" || rep.Values["debug"] != "false" {
		t.Errorf("values (defaults in, nothing invented): %v", rep.Values)
	}

	// a swapped file, a wrong key, no key: no page
	if r := BuildSettings(append([]byte("// x\n"), data...), sig, key, nil, "ru"); r.OK || r.Code != CodeSettingsBadSig || r.HTML != "" {
		t.Errorf("a changed script: %+v", r)
	}
	_, _, other := signedFile(t, settingsScript)
	if r := BuildSettings(data, sig, other, nil, "ru"); r.OK || r.Code != CodeSettingsBadSig {
		t.Errorf("another key: %+v", r)
	}
	if r := BuildSettings(data, sig, "", nil, "ru"); r.OK || r.Code != CodeSettingsNoKey {
		t.Errorf("no key: %+v", r)
	}
	if r := BuildSettings([]byte("not a script ("), nil, key, nil, "ru"); r.OK || r.Code == "" {
		t.Errorf("garbage: %+v", r)
	}

	// nothing to set
	d2, s2, k2 := signedFile(t, `var Transport={info:function(){return{name:"x",params:[]}},open:function(){}}`)
	if r := BuildSettings(d2, s2, k2, nil, "ru"); r.OK || r.Code != CodeSettingsNoSettings {
		t.Errorf("no settings: %+v", r)
	}
}

func TestCustomSettingsPage(t *testing.T) {
	const custom = `
var Transport = {
  info: function () { return { name: "own", params: [{ key: "url", type: "url" }, { key: "k", label: "K", type: "text", default: "d" }] }; },
  settings: function (values) { return { html: "<!doctype html><p>mine " + values.k + "</p>" }; },
  open: function () {}
};`
	data, sig, key := signedFile(t, custom)
	if tr := InspectTrust(data, sig, key, ""); !tr.OK || !tr.SettingsPage {
		t.Errorf("the trust report must say the script has a settings page of its own: %+v", tr)
	}
	if tr := InspectTrust([]byte(settingsScript), nil, "", ""); !tr.OK || tr.SettingsPage {
		t.Errorf("a script without Transport.settings: %+v", tr)
	}
	rep := BuildSettings(data, sig, key, map[string]string{"k": "v"}, "ru")
	if !rep.OK || !rep.Custom || rep.HTML != "<!doctype html><p>mine v</p>" {
		t.Fatalf("custom page: %+v", rep)
	}
	// a custom page is offered even to a script that declares nothing
	d2, s2, k2 := signedFile(t, `var Transport={info:function(){return{name:"x"}},settings:function(){return "<p>hi</p>"},open:function(){}}`)
	if r := BuildSettings(d2, s2, k2, nil, "ru"); !r.OK || r.HTML != "<p>hi</p>" {
		t.Errorf("string result: %+v", r)
	}

	for name, src := range map[string]string{
		"returns nothing": `var Transport={info:function(){return{name:"x"}},settings:function(){},open:function(){}}`,
		"throws":          `var Transport={info:function(){return{name:"x"}},settings:function(){throw new Error("boom")},open:function(){}}`,
		"reaches out":     `var Transport={info:function(){return{name:"x"}},settings:function(){return http.fetch({url:"https://example.com/"})},open:function(){}}`,
		"never returns":   `var Transport={info:function(){return{name:"x"}},settings:function(){while(true){}},open:function(){}}`,
		"too big":         `var Transport={info:function(){return{name:"x"}},settings:function(){return new Array(600*1024).join("a")},open:function(){}}`,
	} {
		d, s, k := signedFile(t, src)
		if r := BuildSettings(d, s, k, nil, "ru"); r.OK || r.Code != CodeSettingsFailed || r.Error == "" {
			t.Errorf("%s: %+v, want a refusal with a reason", name, r)
		}
	}
}
