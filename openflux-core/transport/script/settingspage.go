package script

import (
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/dop251/goja"
)

// The settings wizard: the page an app opens when the user taps "Настройки"
// on a script transport. The script only declares its settings (info().params,
// see Param); this turns the declaration into a page, so every app, and
// scripttest, shows the same thing and a script author writes no HTML. The page
// is the same kind of page as a script's own setup page: it hands the values
// back through window.openfluxSubmit, and the app saves them for the script.
//
// A script that wants a page of its own defines Transport.settings(values) and
// returns HTML instead (see InspectSettings); the generated one is the default.

// PageOptions tune SettingsPage.
type PageOptions struct {
	// Lang is "ru" (the default) or "en": the words the page itself adds
	// (buttons, error messages). Labels and help are the script's own.
	Lang string
}

type pageText struct {
	Settings, Save, Reset, Advanced, Required, NeedNumber, Min, Max, Choose, NeedURL, Pattern string
	FixErrors, Saved, AppOnly, Show, Hide, Nothing                                            string
}

var pageTexts = map[string]pageText{
	"ru": {
		Settings: "Настройки", Save: "Сохранить", Reset: "По умолчанию", Advanced: "Дополнительно",
		Required: "обязательное поле", NeedNumber: "нужно число", Min: "не меньше ", Max: "не больше ",
		Choose: "выберите одно из предложенных", NeedURL: "нужен адрес вида https://…", Pattern: "не подходит под формат",
		FixErrors: "Проверьте поля, отмеченные красным", Saved: "Сохранено",
		AppOnly: "Сохранение работает только внутри приложения OpenFlux", Show: "показать", Hide: "скрыть",
		Nothing: "У этого транспорта нет настроек.",
	},
	"en": {
		Settings: "Settings", Save: "Save", Reset: "Defaults", Advanced: "Advanced",
		Required: "required", NeedNumber: "a number is needed", Min: "at least ", Max: "at most ",
		Choose: "pick one of the choices", NeedURL: "an address like https://… is needed", Pattern: "does not match the format",
		FixErrors: "Fix the fields marked red", Saved: "Saved",
		AppOnly: "Saving only works inside the OpenFlux app", Show: "show", Hide: "hide",
		Nothing: "This transport has no settings.",
	},
}

// SettingsPage renders the settings wizard for every param info declares,
// profile param included: the profile editor's value field and this page edit
// the same value, so a script with only a profile param still gets a page,
// and one with both kinds shows all of them here. Prefilled from values (a
// missing value shows the declared default).
func SettingsPage(info Info, values map[string]string, o PageOptions) string {
	tx, ok := pageTexts[o.Lang]
	if !ok {
		tx = pageTexts["ru"]
		o.Lang = "ru"
	}
	params := info.ResolvedParams()

	var body strings.Builder
	var plain, advanced []int
	for k, p := range params {
		if p.Advanced {
			advanced = append(advanced, k)
		} else {
			plain = append(plain, k)
		}
	}
	renderFields(&body, params, plain, values, tx)
	if len(advanced) > 0 {
		body.WriteString(`<details class="adv"><summary>` + html.EscapeString(tx.Advanced) + `</summary>`)
		renderFields(&body, params, advanced, values, tx)
		body.WriteString(`</details>`)
	}
	if len(params) == 0 {
		body.WriteString(`<p class="nothing">` + html.EscapeString(tx.Nothing) + `</p>`)
	}

	title := info.Name
	if title == "" {
		title = "OpenFlux"
	}
	sub := tx.Settings
	if info.Version != "" {
		sub += " · v" + info.Version
	}
	current := map[string]string{}
	for _, p := range params {
		if v, ok := values[p.Key]; ok {
			current[p.Key] = v
		}
	}
	data := map[string]interface{}{"params": params, "values": current, "text": tx}

	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="` + o.Lang + `"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString(`<title>` + html.EscapeString(title+" — "+tx.Settings) + `</title><style>` + settingsCSS + `</style></head><body><main class="card">`)
	b.WriteString(`<header><h1>` + html.EscapeString(title) + `</h1><p class="sub">` + html.EscapeString(sub) + `</p></header>`)
	b.WriteString(`<form id="f" novalidate>` + body.String())
	b.WriteString(`<div class="bar"><button type="button" id="reset" class="ghost">` + html.EscapeString(tx.Reset) + `</button>`)
	b.WriteString(`<span id="msg" role="status"></span><button type="submit" id="save" class="primary">` + html.EscapeString(tx.Save) + `</button></div></form></main>`)
	b.WriteString(`<script>var DATA=` + jsonForScript(data) + `;` + settingsJS + `</script></body></html>`)
	return b.String()
}

func renderFields(b *strings.Builder, params []Param, idx []int, values map[string]string, tx pageText) {
	lastGroup := ""
	for _, k := range idx {
		p := params[k]
		if p.Group != "" && p.Group != lastGroup {
			b.WriteString(`<h2 class="group">` + html.EscapeString(p.Group) + `</h2>`)
		}
		lastGroup = p.Group
		v, set := values[p.Key]
		if !set {
			v = p.Default
		}
		id := "f" + strconv.Itoa(k)
		label := p.Label
		if label == "" {
			label = p.Key
		}
		req := ""
		if p.Required && p.Type != ParamBoolean {
			req = `<span class="req" title="` + html.EscapeString(tx.Required) + `">*</span>`
		}
		ph := ""
		if p.Placeholder != "" {
			ph = ` placeholder="` + html.EscapeString(p.Placeholder) + `"`
		}
		b.WriteString(`<div class="field" data-k="` + strconv.Itoa(k) + `">`)
		switch p.Type {
		case ParamBoolean:
			checked := ""
			if normalizeBool(v) == "true" {
				checked = " checked"
			}
			b.WriteString(`<label class="switch"><input type="checkbox" id="` + id + `"` + checked + `><span class="track"></span><span>` + html.EscapeString(label) + `</span></label>`)
		case ParamSelect:
			b.WriteString(`<label for="` + id + `">` + html.EscapeString(label) + req + `</label><select id="` + id + `">`)
			if !p.Required || v == "" {
				b.WriteString(`<option value=""></option>`)
			}
			for _, o := range p.Options {
				ol := o.Label
				if ol == "" {
					ol = o.Value
				}
				sel := ""
				if o.Value == v {
					sel = " selected"
				}
				b.WriteString(`<option value="` + html.EscapeString(o.Value) + `"` + sel + `>` + html.EscapeString(ol) + `</option>`)
			}
			b.WriteString(`</select>`)
		case ParamTextarea:
			b.WriteString(`<label for="` + id + `">` + html.EscapeString(label) + req + `</label><textarea id="` + id + `" rows="4"` + ph + `>` + html.EscapeString(v) + `</textarea>`)
		case ParamSecret:
			b.WriteString(`<label for="` + id + `">` + html.EscapeString(label) + req + `</label><div class="pw"><input id="` + id + `" type="password" autocomplete="off" spellcheck="false"` + ph + ` value="` + html.EscapeString(v) + `"><button type="button" class="ghost sm" data-show="` + id + `">` + html.EscapeString(tx.Show) + `</button></div>`)
		case ParamNumber:
			b.WriteString(`<label for="` + id + `">` + html.EscapeString(label) + req + `</label><input id="` + id + `" type="text" inputmode="decimal" autocomplete="off"` + ph + ` value="` + html.EscapeString(v) + `">`)
		default: // url, text, and any type this core does not know
			typ := "text"
			if p.Type == ParamURL {
				typ = "url"
			}
			b.WriteString(`<label for="` + id + `">` + html.EscapeString(label) + req + `</label><input id="` + id + `" type="` + typ + `" autocomplete="off" spellcheck="false"` + ph + ` value="` + html.EscapeString(v) + `">`)
		}
		if p.Description != "" {
			b.WriteString(`<div class="desc">` + html.EscapeString(p.Description) + `</div>`)
		}
		b.WriteString(`<div class="err" aria-live="polite"></div></div>`)
	}
}

// jsonForScript is JSON that is safe inside a <script> element.
func jsonForScript(v interface{}) string {
	b, _ := json.Marshal(v)
	s := string(b)
	s = strings.NewReplacer("<", `<`, ">", `>`, "&", `&`, " ", ` `, " ", ` `).Replace(s)
	return s
}

const settingsCSS = `:root{--bg:#0f1115;--card:#171a21;--text:#e8eaed;--muted:#9aa0a6;--accent:#4c9aff;--ok:#3ddc84;--bad:#ff6b6b;--border:#262a33;--field:#10131a}
@media (prefers-color-scheme:light){:root{--bg:#f4f5f7;--card:#fff;--text:#1b1f24;--muted:#5f6368;--accent:#2f6fe4;--ok:#1c8a4b;--bad:#c62828;--border:#e3e5e8;--field:#f8f9fb}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:15px/1.45 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
.card{max-width:640px;margin:0 auto;padding:20px 18px 28px}header{margin-bottom:14px}h1{font-size:20px;margin:0 0 2px}.sub{margin:0;color:var(--muted);font-size:13px}
.group{font-size:12px;letter-spacing:.06em;text-transform:uppercase;color:var(--muted);margin:22px 0 4px}
.field{margin:14px 0}label{display:block;font-weight:600;font-size:14px;margin-bottom:5px}.req{color:var(--bad);margin-left:3px}
input[type=text],input[type=url],input[type=password],select,textarea{width:100%;background:var(--field);color:var(--text);border:1px solid var(--border);border-radius:10px;padding:10px 12px;font:inherit}
textarea{resize:vertical;min-height:84px}input:focus,select:focus,textarea:focus{outline:2px solid var(--accent);outline-offset:-1px}
.field.bad input,.field.bad select,.field.bad textarea{border-color:var(--bad)}.desc{color:var(--muted);font-size:13px;margin-top:5px}.err{color:var(--bad);font-size:13px;min-height:0;margin-top:4px}.err:empty{display:none}
.pw{display:flex;gap:8px}.pw input{flex:1}.switch{display:flex;align-items:center;gap:10px;font-weight:600;cursor:pointer}.switch input{position:absolute;opacity:0}
.track{width:40px;height:22px;border-radius:11px;background:var(--border);position:relative;flex:none;transition:background .15s}.track:after{content:"";position:absolute;left:3px;top:3px;width:16px;height:16px;border-radius:50%;background:#fff;transition:transform .15s}
.switch input:checked+.track{background:var(--accent)}.switch input:checked+.track:after{transform:translateX(18px)}.switch input:focus-visible+.track{outline:2px solid var(--accent);outline-offset:2px}
.adv{margin-top:22px;border-top:1px solid var(--border);padding-top:10px}.adv summary{cursor:pointer;color:var(--muted);font-weight:600}
.bar{display:flex;align-items:center;gap:12px;margin-top:26px;position:sticky;bottom:0;background:linear-gradient(transparent,var(--bg) 30%);padding:14px 0 4px}
#msg{flex:1;font-size:13px;color:var(--muted)}#msg.bad{color:var(--bad)}#msg.ok{color:var(--ok)}
button{font:inherit;border-radius:10px;border:1px solid var(--border);padding:9px 16px;cursor:pointer;color:var(--text);background:transparent}
button.primary{background:var(--accent);border-color:var(--accent);color:#fff;font-weight:600}button.sm{padding:6px 10px;font-size:13px}button:disabled{opacity:.55;cursor:default}.nothing{color:var(--muted)}`

const settingsJS = `(function(){
var P=DATA.params,V=DATA.values,T=DATA.text,form=document.getElementById('f'),msg=document.getElementById('msg');
function el(k){return document.getElementById('f'+k)}
function field(k){return el(k).closest('.field')}
function read(k){var p=P[k],e=el(k);if(p.type==='boolean')return e.checked?'true':'false';
 var v=e.value;if(p.type==='secret'||p.type==='textarea')return v;v=v.trim();if(p.type==='number')v=v.replace(',','.');return v}
function check(p,v){
 if(p.type==='number'){var n=Number(v);if(v===''||!isFinite(n))return T.NeedNumber;if(p.min!=null&&n<p.min)return T.Min+p.min;if(p.max!=null&&n>p.max)return T.Max+p.max}
 else if(p.type==='select'){if(!(p.options||[]).some(function(o){return o.value===v}))return T.Choose}
 else if(p.type==='url'){if(v.indexOf('://')<0)return T.NeedURL}
 if(p.pattern&&p.type!=='boolean'&&p.type!=='select'&&p.type!=='number'){try{if(!new RegExp(p.pattern).test(v))return T.Pattern}catch(e){}}
 return ''}
function validateOne(k){var p=P[k],v=read(k),m='';
 if(p.type!=='boolean'){if(v===''){if(p.required)m=T.Required}else m=check(p,v)}
 var f=field(k);f.classList.toggle('bad',!!m);f.querySelector('.err').textContent=m;return !m}
function validate(){var ok=true,first=null;for(var k=0;k<P.length;k++){if(!validateOne(k)){ok=false;if(!first)first=k}}
 if(first!==null){var f=field(first);var d=f.closest('details');if(d)d.open=true;el(first).focus()}return ok}
function say(t,c){msg.textContent=t||'';msg.className=c||''}
function set(k,v){var p=P[k],e=el(k);if(p.type==='boolean')e.checked=(v==='true');else e.value=v}
form.addEventListener('submit',function(ev){ev.preventDefault();
 if(!validate()){say(T.FixErrors,'bad');return}
 var out={};for(var k=0;k<P.length;k++)out[P[k].key]=read(k);
 if(typeof window.openfluxSubmit==='function'){say('','');window.openfluxSubmit(out);say(T.Saved,'ok')}else say(T.AppOnly,'bad')});
document.getElementById('reset').addEventListener('click',function(){
 for(var k=0;k<P.length;k++){set(k,P[k].default||(P[k].type==='boolean'?'false':''));field(k).classList.remove('bad');field(k).querySelector('.err').textContent=''}say('','')});
form.addEventListener('input',function(ev){var f=ev.target.closest&&ev.target.closest('.field');if(f){say('','');if(f.classList.contains('bad'))validateOne(+f.dataset.k)}});
form.addEventListener('focusout',function(ev){var f=ev.target.closest&&ev.target.closest('.field');if(f&&ev.target.tagName!=='BUTTON')validateOne(+f.dataset.k)});
Array.prototype.forEach.call(document.querySelectorAll('[data-show]'),function(b){b.addEventListener('click',function(){
 var i=document.getElementById(b.getAttribute('data-show')),h=i.type==='password';i.type=h?'text':'password';b.textContent=h?T.Hide:T.Show})});
})();`

// InspectSettings asks the script for a settings page of its own: a script
// that defines Transport.settings(values) and returns an HTML string (or
// {html}) is shown that instead of the generated wizard. It runs like
// Inspect does (no network, no sockets, three seconds), since it is called
// from the apps before anything about the script is connected; so the page
// must be plain data and may reach the world only through its own scripts in
// the browser. custom is false when the script defines no such function.
func InspectSettings(src []byte, values map[string]string) (page string, custom bool, err error) {
	vm := goja.New()
	registerInspectAPI(vm)
	stop := inspectTimer(vm)
	defer stop()
	if _, err := vm.RunString(string(src)); err != nil {
		return "", false, fmt.Errorf("eval: %w", err)
	}
	obj, ok := vm.Get("Transport").(*goja.Object)
	if !ok || obj == nil {
		return "", false, fmt.Errorf("script must define a global `Transport` object")
	}
	fn, ok := goja.AssertFunction(obj.Get("settings"))
	if !ok {
		return "", false, nil
	}
	arg := vm.NewObject()
	for k, v := range values {
		_ = arg.Set(k, v)
	}
	res, err := fn(obj, arg)
	if err != nil {
		return "", true, fmt.Errorf("Transport.settings(): %w", err)
	}
	switch x := res.Export().(type) {
	case string:
		page = x
	case map[string]interface{}:
		page, _ = x["html"].(string)
	}
	if page == "" {
		return "", true, fmt.Errorf("Transport.settings() must return an HTML string or {html}")
	}
	if len(page) > maxSetupHTML {
		return "", true, fmt.Errorf("Transport.settings(): the page is %d bytes, the limit is %d", len(page), maxSetupHTML)
	}
	return page, true, nil
}

// SettingsReport is what an app gets when it asks for a script's settings
// page: always printed as JSON, ok false with a code and a reason on failure.
type SettingsReport struct {
	OK        bool    `json:"ok"`
	Signature string  `json:"signature"`
	Code      string  `json:"code,omitempty"`
	Error     string  `json:"error,omitempty"`
	Name      string  `json:"name,omitempty"`
	Version   string  `json:"version,omitempty"`
	Params    []Param `json:"params"`
	Custom    bool    `json:"custom,omitempty"`
	HTML      string  `json:"html,omitempty"`
	// Values are the current values with the declared defaults filled in, what
	// the script would get as cfg.params right now.
	Values map[string]string `json:"values,omitempty"`
}

// Codes of SettingsReport.Code.
const (
	CodeSettingsNoKey      = "no_key"
	CodeSettingsBadSig     = "bad_signature"
	CodeSettingsNoSettings = "no_settings"
	CodeSettingsFailed     = "failed"
)

// BuildSettings is the apps' entry: it verifies the script against the key the
// user pinned (a swapped file fails closed, like loading it does), reads its
// declaration, and returns the settings page (the script's own, else the
// generated wizard, covering every declared param including the profile one)
// for the current values. A script with no params at all reports
// CodeSettingsNoSettings (and no page) unless it brings its own.
func BuildSettings(data, sig []byte, pubkeyHex string, values map[string]string, lang string) SettingsReport {
	rep := SettingsReport{Signature: "unverified", Params: []Param{}}
	if strings.TrimSpace(pubkeyHex) == "" {
		rep.Code, rep.Error = CodeSettingsNoKey, "нужен закреплённый ключ автора"
		return rep
	}
	trust := InspectTrust(data, sig, pubkeyHex, "")
	rep.Signature = trust.Signature
	if !trust.OK {
		rep.Code, rep.Error = CodeSettingsFailed, trust.Error
		if trust.Code != "" {
			rep.Code = trust.Code
		}
		return rep
	}
	if trust.Signature != "valid" {
		rep.Code, rep.Error = CodeSettingsBadSig, "подпись не сходится с закреплённым ключом автора"
		return rep
	}
	src := data
	if len(data) >= 2 && data[0] == 'P' && data[1] == 'K' {
		pkg, err := ReadPackage(data)
		if err != nil {
			rep.Code, rep.Error = CodeSettingsFailed, "пакет .flux: "+err.Error()
			return rep
		}
		src = pkg.Script
	}
	info, err := Inspect(src)
	if err != nil {
		rep.Code, rep.Error = CodeSettingsFailed, err.Error()
		return rep
	}
	rep.Name, rep.Version = info.Name, info.Version
	rep.Params = info.ResolvedParams()
	if rep.Params == nil {
		rep.Params = []Param{}
	}
	rep.Values, _ = NormalizeSettings(rep.Params, values)

	page, custom, err := InspectSettings(src, rep.Values)
	if err != nil {
		rep.Code, rep.Error = CodeSettingsFailed, err.Error()
		return rep
	}
	switch {
	case custom:
		rep.Custom, rep.HTML = true, page
	case len(rep.Params) == 0:
		rep.Code, rep.Error = CodeSettingsNoSettings, "у транспорта нет настроек"
		return rep
	default:
		rep.HTML = SettingsPage(info, rep.Values, PageOptions{Lang: lang})
	}
	rep.OK = true
	return rep
}
