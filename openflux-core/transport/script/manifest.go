// Package script hosts JS-defined transports inside a per-transport goja
// runtime. A script transport is a signed .js file that implements the
// entire transport (auth flow, wire framing, reconnect/keepalive policy)
// on top of a small host API this package injects into the runtime; the Go
// side only does real I/O (HTTP, WebSocket, timers) and pumps callbacks
// into the script's own event loop. See js/template.js for the contract.
package script

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/dop251/goja"
)

// Param describes one input a script transport asks the user for, or one
// setting it exposes. It is pure declaration: the apps build their forms from
// it (the profile editor for the primary input, the settings wizard page for
// the rest, see settingspage.go), and the script gets the values back in
// cfg.url / cfg.params - always as strings.
//
// Only Key, Label, Type and Required existed at first; the rest is optional
// and an older script that does not set it behaves exactly as before.
type Param struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type"` // ParamURL | ParamText | ParamSecret | ParamNumber | ParamBoolean | ParamSelect | ParamTextarea
	Required bool   `json:"required"`

	// Scope marks which one param (at most one) is also the profile editor's
	// value field, delivered as cfg.url: "profile" for that one, "settings" for
	// every other. The settings wizard shows and saves ALL of them regardless -
	// a "profile" param is just the one that also gets a quick field at profile
	// creation time, both ends reading/writing the one saved value. Left empty,
	// the first param is the profile's and the rest are settings, as the apps
	// always treated them.
	Scope string `json:"scope,omitempty"`
	// Default is what cfg.params carries until the user sets something; a
	// boolean's is "true" or "false". Declared as a string, number or boolean.
	Default string `json:"default,omitempty"`
	// Description is the help text under the field; Placeholder the grey hint in it.
	Description string `json:"description,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	// Options are a select's choices, declared as ["a", "b"] or [{value, label}].
	Options []ParamOption `json:"options,omitempty"`
	// Min and Max bound a number.
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
	// Pattern is a regular expression a text or url value must match (the
	// page checks it as a JavaScript RegExp, the core's checks as RE2: keep
	// to the common subset).
	Pattern string `json:"pattern,omitempty"`
	// Group is a section title; fields with the same Group sit together.
	Group string `json:"group,omitempty"`
	// Advanced folds the field under "Дополнительно".
	Advanced bool `json:"advanced,omitempty"`
}

// ParamOption is one choice of a select param.
type ParamOption struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// The param types. An unknown type is treated as ParamText, so a script
// written for a newer core still gets a working (if plainer) form.
const (
	ParamURL      = "url"
	ParamText     = "text"
	ParamSecret   = "secret"
	ParamNumber   = "number"
	ParamBoolean  = "boolean"
	ParamSelect   = "select"
	ParamTextarea = "textarea"
)

// UnmarshalJSON reads what a script's info() returns: a default may be a
// string, a number or a boolean, and options may be plain strings.
func (p *Param) UnmarshalJSON(b []byte) error {
	type plain Param
	aux := struct {
		*plain
		Default interface{}       `json:"default"`
		Options []json.RawMessage `json:"options"`
	}{plain: (*plain)(p)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	p.Default = ""
	switch d := aux.Default.(type) {
	case nil:
	case string:
		p.Default = d
	case bool:
		p.Default = strconv.FormatBool(d)
	case float64:
		p.Default = strconv.FormatFloat(d, 'f', -1, 64)
	default:
		return fmt.Errorf("param %q: default must be a string, a number or a boolean", p.Key)
	}
	p.Options = nil
	for _, raw := range aux.Options {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			p.Options = append(p.Options, ParamOption{Value: s})
			continue
		}
		var o ParamOption
		if err := json.Unmarshal(raw, &o); err != nil {
			return fmt.Errorf("param %q: an option is a string or {value, label}", p.Key)
		}
		p.Options = append(p.Options, o)
	}
	return nil
}

// Info is the manifest a script transport returns from Transport.info().
//
// MTU/Reliable/Ordered/HalfDuplex/MinInterval are advisory only: the core
// never fragments, reorders, or rate-limits on a script transport's behalf
// unless it explicitly opts in. The default is straight passthrough - every
// native transport ported so far (mailru included) ships whatever []byte
// it's handed as a single frame, uncapped, and script transports do the same
// unless a future transport's manifest asks for something else.
type Info struct {
	Name         string
	Version      string
	CookieDomain string // base URL cookieJar.get()/set() operate against
	MTU          int    // 0 = unbounded (default; core does not fragment)
	Reliable     bool
	Ordered      bool
	HalfDuplex   bool
	MinInterval  time.Duration
	Params       []Param
	// CustomSettings: the script defines Transport.settings(values), a
	// settings page of its own (see InspectSettings). Set by Inspect, not by
	// info().
	CustomSettings bool

	// ScopeCookiesToParentDomain: when ApplyCookies (the generic Go-side
	// CookieExchanger - see transport.go) applies externally-supplied
	// cookies, scope them to the parent domain (disk.yandex.ru ->
	// yandex.ru) instead of the exact CookieDomain host. Some doc-collab
	// flows redirect a captcha/login solve across sibling subdomains, so a
	// host-only cookie never reaches the host that actually needed it.
	// Default false: unset, existing scripts (mailru) keep exact host-only
	// scoping, unchanged.
	ScopeCookiesToParentDomain bool

	// HTTP transport pool tuning, applied once info() is parsed and before
	// open() runs (see transport.go's tuneHTTPTransport). Go's http.Transport
	// defaults (2 idle conns/host) throttle a script that fans out many
	// concurrent requests via concurrency.pool - a throughput-oriented
	// transport (Volga-style batch relay) sets these; a plain doc-transport
	// leaves them at 0 and gets the same small-pool default every script
	// had before this field existed.
	HTTPMaxConnsPerHost int
	HTTPMaxIdleConns    int
	HTTPIdleConnTimeout time.Duration

	// Auth is the sign-in the transport needs (see Auth); nil: none.
	Auth *Auth

	// BatchBytes asks the Session to coalesce packets into messages of up to
	// this many bytes (transport.BatchSizeHinter): for a carrier limited by
	// messages, not bytes (Mail.ru's chat: ~500 KB). 0: the default.
	BatchBytes int
}

// rawInfo mirrors the JSON shape scripts return from info(); durations cross
// the JS boundary as plain milliseconds since goja has no Duration type.
type rawInfo struct {
	Name                       string  `json:"name"`
	Version                    string  `json:"version"`
	CookieDomain               string  `json:"cookieDomain"`
	MTU                        int     `json:"mtu"`
	Reliable                   bool    `json:"reliable"`
	Ordered                    bool    `json:"ordered"`
	HalfDuplex                 bool    `json:"halfDuplex"`
	MinIntervalMs              float64 `json:"minIntervalMs"`
	Params                     []Param `json:"params"`
	ScopeCookiesToParentDomain bool    `json:"scopeCookiesToParentDomain"`
	HTTPMaxConnsPerHost        int     `json:"httpMaxConnsPerHost"`
	HTTPMaxIdleConns           int     `json:"httpMaxIdleConns"`
	HTTPIdleConnTimeoutMs      float64 `json:"httpIdleConnTimeoutMs"`
	Auth                       *Auth   `json:"auth"`
	BatchBytes                 int     `json:"batchBytes"`
}

func parseInfo(v goja.Value) (Info, error) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return Info{}, fmt.Errorf("Transport.info() returned nothing")
	}
	// Round-trip through JSON instead of walking goja.Object by hand: info()
	// is a plain data object, and every field it can legally carry (string,
	// number, bool, array of objects) survives the round-trip intact.
	b, err := json.Marshal(v.Export())
	if err != nil {
		return Info{}, fmt.Errorf("Transport.info(): export: %w", err)
	}
	var raw rawInfo
	if err := json.Unmarshal(b, &raw); err != nil {
		return Info{}, fmt.Errorf("Transport.info(): decode: %w", err)
	}
	if raw.Name == "" {
		return Info{}, fmt.Errorf("Transport.info() must set `name`")
	}
	return Info{
		Name:                       raw.Name,
		Version:                    raw.Version,
		CookieDomain:               raw.CookieDomain,
		MTU:                        raw.MTU,
		Reliable:                   raw.Reliable,
		Ordered:                    raw.Ordered,
		HalfDuplex:                 raw.HalfDuplex,
		MinInterval:                time.Duration(raw.MinIntervalMs * float64(time.Millisecond)),
		Params:                     raw.Params,
		ScopeCookiesToParentDomain: raw.ScopeCookiesToParentDomain,
		HTTPMaxConnsPerHost:        raw.HTTPMaxConnsPerHost,
		HTTPMaxIdleConns:           raw.HTTPMaxIdleConns,
		HTTPIdleConnTimeout:        time.Duration(raw.HTTPIdleConnTimeoutMs * float64(time.Millisecond)),
		Auth:                       raw.Auth,
		BatchBytes:                 raw.BatchBytes,
	}, nil
}
