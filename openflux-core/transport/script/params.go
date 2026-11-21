package script

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Scope values of Param.Scope.
const (
	ScopeProfile  = "profile"
	ScopeSettings = "settings"
)

// scopes resolves where each of info.Params is asked for. An explicit Scope
// wins; without one, the first param is the profile's one input (as the apps
// always treated it) and the rest are settings. At most one param is the
// profile's: a second "profile" falls back to settings.
func (i Info) scopes() []string {
	out := make([]string, len(i.Params))
	explicit := false
	for _, p := range i.Params {
		if p.Scope == ScopeProfile {
			explicit = true
		}
	}
	seen := false
	for k, p := range i.Params {
		switch {
		case p.Scope == ScopeProfile && !seen:
			out[k], seen = ScopeProfile, true
		case p.Scope != "":
			out[k] = ScopeSettings
		case k == 0 && !explicit:
			out[k], seen = ScopeProfile, true
		default:
			out[k] = ScopeSettings
		}
	}
	return out
}

// ResolvedParams is Params with every Scope filled in ("profile" or
// "settings") by the rule above, so an app reads the answer instead of
// repeating the rule.
func (i Info) ResolvedParams() []Param {
	out := append([]Param(nil), i.Params...)
	for k, sc := range i.scopes() {
		out[k].Scope = sc
	}
	return out
}

// ProfileParam is the param the profile editor's value field stands for
// (cfg.url), nil if the script has none.
func (i Info) ProfileParam() *Param {
	for k, sc := range i.scopes() {
		if sc == ScopeProfile {
			p := i.Params[k]
			return &p
		}
	}
	return nil
}

// SettingParams are the params the settings wizard asks for (cfg.params).
func (i Info) SettingParams() []Param {
	var out []Param
	for k, sc := range i.scopes() {
		if sc == ScopeSettings {
			out = append(out, i.Params[k])
		}
	}
	return out
}

// FieldError is one reason a value cannot be accepted.
type FieldError struct {
	Key     string `json:"key"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Key + ": " + e.Message }

// NormalizeSettings applies the declared defaults to the values and checks
// them against the declaration: required, number and its bounds, select
// options, pattern. What it returns is what the script gets in cfg.params:
// strings only, a boolean as "true"/"false", text trimmed. Keys the script
// did not declare are dropped. Errors do not stop the normalizing; callers
// that only want defaults (the transport at start) ignore them.
func NormalizeSettings(params []Param, values map[string]string) (map[string]string, []FieldError) {
	out := make(map[string]string, len(params))
	var errs []FieldError
	for _, p := range params {
		v, given := values[p.Key]
		if !given {
			v = p.Default
		}
		switch p.Type {
		case ParamBoolean:
			v = normalizeBool(v)
		case ParamSecret, ParamTextarea:
			// kept as typed: a secret's edge spaces may be part of it
		default:
			v = strings.TrimSpace(v)
		}
		if v == "" && p.Type != ParamBoolean {
			if p.Required {
				errs = append(errs, FieldError{p.Key, "обязательное поле"})
			}
			out[p.Key] = ""
			continue
		}
		if msg := checkValue(p, v); msg != "" {
			errs = append(errs, FieldError{p.Key, msg})
		}
		out[p.Key] = v
	}
	return out, errs
}

// WithDefaults fills in the declared default of every param the values do not
// set (or leave empty) and drops nothing else: the cheap half of
// NormalizeSettings, for a transport that is starting.
func WithDefaults(params []Param, values map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(values)+len(params))
	for k, v := range values {
		out[k] = v
	}
	for _, p := range params {
		if p.Default == "" {
			continue
		}
		if cur, ok := out[p.Key]; !ok || cur == nil || cur == "" {
			out[p.Key] = p.Default
		}
	}
	return out
}

func normalizeBool(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "on", "yes", "да":
		return "true"
	}
	return "false"
}

// checkValue is "" when v (not empty) is fine for p.
func checkValue(p Param, v string) string {
	switch p.Type {
	case ParamNumber:
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return "нужно число"
		}
		if p.Min != nil && n < *p.Min {
			return "не меньше " + strconv.FormatFloat(*p.Min, 'f', -1, 64)
		}
		if p.Max != nil && n > *p.Max {
			return "не больше " + strconv.FormatFloat(*p.Max, 'f', -1, 64)
		}
	case ParamSelect:
		for _, o := range p.Options {
			if o.Value == v {
				return ""
			}
		}
		return "выберите одно из предложенных"
	case ParamURL:
		if !strings.Contains(v, "://") {
			return "нужен адрес вида https://…"
		}
	}
	if p.Pattern != "" && p.Type != ParamBoolean && p.Type != ParamSelect && p.Type != ParamNumber {
		if re, err := regexp.Compile(p.Pattern); err == nil && !re.MatchString(v) {
			return "не подходит под формат"
		}
	}
	return ""
}

// CheckParams lists what is wrong or suspicious in a script's declaration, for
// its author (scripttest and --inspect-script print them): a duplicate or
// empty key, an unknown type, a select without options, a default that its own
// rules refuse, a bad pattern, a number bounded the wrong way round. None of it
// stops a script from loading; the apps degrade to a plain text field.
func (i Info) CheckParams() []string {
	var out []string
	seen := map[string]bool{}
	for k, p := range i.Params {
		at := fmt.Sprintf("params[%d]", k)
		if p.Key != "" {
			at = fmt.Sprintf("param %q", p.Key)
		}
		if p.Key == "" {
			out = append(out, at+": no key")
			continue
		}
		if seen[p.Key] {
			out = append(out, at+": the key is declared twice")
		}
		seen[p.Key] = true
		switch p.Type {
		case ParamURL, ParamText, ParamSecret, ParamNumber, ParamBoolean, ParamSelect, ParamTextarea:
		case "":
			out = append(out, at+": no type (a text field is shown)")
		default:
			out = append(out, fmt.Sprintf("%s: unknown type %q (a text field is shown)", at, p.Type))
		}
		if p.Label == "" {
			out = append(out, at+": no label (the key is shown)")
		}
		if p.Scope != "" && p.Scope != ScopeProfile && p.Scope != ScopeSettings {
			out = append(out, fmt.Sprintf("%s: scope must be %q or %q, not %q", at, ScopeProfile, ScopeSettings, p.Scope))
		}
		if p.Type == ParamSelect && len(p.Options) == 0 {
			out = append(out, at+": a select needs options")
		}
		if p.Min != nil && p.Max != nil && *p.Min > *p.Max {
			out = append(out, at+": min is greater than max")
		}
		if p.Pattern != "" {
			if _, err := regexp.Compile(p.Pattern); err != nil {
				out = append(out, at+": pattern does not compile: "+err.Error())
			}
		}
		if p.Default != "" && p.Scope != ScopeProfile {
			if msg := checkValue(p, p.Default); msg != "" && !(p.Type == ParamBoolean) {
				out = append(out, fmt.Sprintf("%s: its default %q is refused by its own rules (%s)", at, p.Default, msg))
			}
		}
	}
	return out
}

// EncodeSettings and DecodeSettings carry a script's saved settings through a
// place that only holds a plain line: the app's generated .conf, where a value
// ends at '#' or ';' and cannot hold a newline (a text setting can). The line
// is the settings as a JSON object of strings, base64url without padding.
func EncodeSettings(values map[string]string) string {
	b, _ := json.Marshal(values)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeSettings is EncodeSettings's inverse.
func DecodeSettings(line string) (map[string]interface{}, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(line), "="))
	if err != nil {
		return nil, fmt.Errorf("the saved settings are not base64url: %w", err)
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("the saved settings are not a JSON object of strings: %w", err)
	}
	out := make(map[string]interface{}, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out, nil
}
