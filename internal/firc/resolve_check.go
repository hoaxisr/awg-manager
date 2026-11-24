package firc

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Правила resolve и групп «только DNS» те же, что у fircd (resolver_addr.c, resolve_check.c,
// firc_group_dns_check): одно нарушение в groups.yaml — и демон не загрузит файл целиком.

const (
	maxResolveServers    = 4
	maxResolveURLBytes   = 255
	defaultAddressPoolV4 = "198.18.0.0/15"
)

// GroupValidationError — группа, которую fircd не примет. Field — поле API демона.
type GroupValidationError struct {
	Group string
	Field string
	Msg   string
}

func (e *GroupValidationError) Error() string {
	return fmt.Sprintf("group %q: %s", e.Group, e.Msg)
}

// ValidateGroups проверяет интерфейсы, resolve и ограничения групп «только DNS».
// pool — префиксы пула подставных адресов fircd: резолвера внутри них быть не может.
func ValidateGroups(groups []Group, pool []netip.Prefix) error {
	for _, g := range groups {
		fail := func(field, msg string) error {
			return &GroupValidationError{Group: g.Name, Field: field, Msg: msg}
		}
		if len(g.Interface) > 1 && slices.Contains(g.Interface, DNSOnlyInterface) {
			return fail("interface", "dns cannot be one of several")
		}
		if err := validateResolveServer(g.Resolve.Server, pool); err != nil {
			return fail("resolve.server", "resolve.server "+err.Error())
		}
		if !g.IsDNSOnly() {
			if g.Resolve.Fallback != nil && !*g.Resolve.Fallback {
				return fail("resolve.fallback", "resolve.fallback can be off only on a dns group")
			}
			continue
		}
		if g.Resolve.Server == "" {
			return fail("resolve.server", "a dns group needs resolve.server")
		}
		if g.Cloudflare {
			return fail("cloudflare", "cloudflare is not for a group whose interface is dns")
		}
		for _, r := range g.Rules {
			if r.Type == "subnet" || r.Type == "subnet6" {
				return fail("rules", "a dns group cannot hold subnet rules")
			}
		}
	}
	return nil
}

func validateResolveServer(list string, pool []netip.Prefix) error {
	if list == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, raw := range strings.Split(list, ",") {
		entry := strings.Trim(raw, " \t")
		if entry == "" {
			return fmt.Errorf("%q has an empty entry", list)
		}
		if len(seen) == maxResolveServers {
			return fmt.Errorf("%q names more than %d servers", list, maxResolveServers)
		}
		key, ip, err := parseResolveServer(entry)
		if err != nil {
			return fmt.Errorf("%q %w", entry, err)
		}
		if ip.IsValid() {
			if ip.Is4In6() {
				return fmt.Errorf("%q is an IPv4-mapped IPv6 address; write the IPv4 address", entry)
			}
			if ip.IsUnspecified() || ip.IsLoopback() {
				return fmt.Errorf("%q is a sink address (0.0.0.0, 127.0.0.0/8, ::, ::1), which no resolver answers from", entry)
			}
			for _, p := range pool {
				if p.Contains(ip) {
					return fmt.Errorf("%q is inside firc's address pool, where no resolver can be", entry)
				}
			}
		}
		if seen[key] {
			return fmt.Errorf("%q names %q twice", list, entry)
		}
		seen[key] = true
	}
	return nil
}

// parseResolveServer разбирает одну запись: ключ для поиска повторов и адрес, если он записан
// литералом (у DoH с именем его нет).
func parseResolveServer(entry string) (string, netip.Addr, error) {
	if strings.Contains(entry, "://") {
		host, ok := dohURLHost(entry)
		if !ok {
			return "", netip.Addr{}, fmt.Errorf("is not a DoH URL (https://host[:port]/path, at most %d bytes, no user, query or fragment)", maxResolveURLBytes)
		}
		ip, _ := netip.ParseAddr(host)
		return entry, ip, nil
	}
	host, port, bracketed, ok := cutHostPort(entry)
	ip, err := netip.ParseAddr(host)
	if !ok || err != nil || ip.Zone() != "" || (bracketed && ip.Is4()) {
		return "", netip.Addr{}, errors.New("is not an address literal (addr, addr:port, [v6addr] or [v6addr]:port)")
	}
	n := 53
	if port != "" {
		if n, ok = parsePort(port); !ok || len(port) > 5 {
			return "", netip.Addr{}, errors.New("has a port outside 1..65535")
		}
	}
	return netip.AddrPortFrom(ip, uint16(n)).String(), ip, nil
}

// cutHostPort делит запись на адрес и порт; у [v6] скобки снимаются. ok=false — запись
// не собрать: скобка не закрыта или после двоеточия пусто.
func cutHostPort(s string) (host, port string, bracketed, ok bool) {
	if strings.HasPrefix(s, "[") {
		host, rest, found := strings.Cut(s[1:], "]")
		if !found || (rest != "" && (rest[0] != ':' || len(rest) == 1)) {
			return "", "", true, false
		}
		return host, strings.TrimPrefix(rest, ":"), true, true
	}
	if strings.Count(s, ":") != 1 {
		return s, "", false, true
	}
	host, port, _ = strings.Cut(s, ":")
	return host, port, false, port != ""
}

func parsePort(s string) (int, bool) {
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 1 && n <= 65535
}

// dohURLHost возвращает хост адреса DoH (без скобок); ok=false — fircd адрес не примет.
func dohURLHost(url string) (string, bool) {
	const scheme = "https://"
	if len(url) > maxResolveURLBytes || len(url) < len(scheme) || !strings.EqualFold(url[:len(scheme)], scheme) {
		return "", false
	}
	for i := 0; i < len(url); i++ {
		if c := url[i]; c <= ' ' || c == 0x7f || c == '?' || c == '#' {
			return "", false
		}
	}
	authority, _, found := strings.Cut(url[len(scheme):], "/")
	host, port, bracketed, ok := cutHostPort(authority)
	if !found || !ok || host == "" || strings.ContainsAny(host, "@[]") {
		return "", false
	}
	if bracketed {
		if ip, err := netip.ParseAddr(host); err != nil || !ip.Is6() || ip.Zone() != "" {
			return "", false
		}
	} else if strings.Contains(host, ":") {
		return "", false
	}
	if port != "" {
		if _, ok := parsePort(port); !ok {
			return "", false
		}
	}
	return host, true
}

// addressPool — префиксы пула подставных адресов fircd: app.addressPool из firc.conf,
// умолчание v4 и сгенерированный демоном префикс v6.
func (o *Operator) addressPool() []netip.Prefix {
	data, _ := os.ReadFile(o.ConfigPath())
	return o.addressPoolFor(data)
}

// addressPoolFor — то же для содержимого firc.conf, которое ещё не на диске (снимок перед восстановлением).
func (o *Operator) addressPoolFor(fircConf []byte) []netip.Prefix {
	var conf struct {
		App struct {
			AddressPool struct {
				V4 struct {
					Pool string `yaml:"pool"`
				} `yaml:"v4"`
				V6 struct {
					Pool string `yaml:"pool"`
				} `yaml:"v6"`
			} `yaml:"addressPool"`
		} `yaml:"app"`
	}
	_ = yaml.Unmarshal(fircConf, &conf) // нечитаемый конфиг не примет и сам fircd: остаются умолчания
	v4, v6 := conf.App.AddressPool.V4.Pool, conf.App.AddressPool.V6.Pool
	if v4 == "" {
		v4 = defaultAddressPoolV4
	}
	if v6 == "" {
		if data, err := os.ReadFile(filepath.Join(o.configDir, "v6-pool-prefix")); err == nil {
			v6, _, _ = strings.Cut(string(data), "\n")
		}
	}
	var out []netip.Prefix
	for _, s := range []string{v4, v6} {
		if p, err := netip.ParsePrefix(strings.TrimSpace(s)); err == nil {
			out = append(out, p)
		}
	}
	return out
}
