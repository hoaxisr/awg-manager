//go:build linux

package wireguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

var legacyIPBinaries sync.Map

func defaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	binary := name
	if name == "ip" || name == "wg" {
		candidate := "/opt/sbin/ip"
		if name == "wg" {
			candidate = "/opt/bin/wg"
		}
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			binary = candidate
		}
	}
	invoke := func(ctx context.Context, path string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, path, args...).Output()
	}
	if name == "ip" {
		return compatibleIP(ctx, binary, args, invoke)
	}
	return invoke(ctx, binary, args...)
}

// JSON is absent from Entware iproute2 4.4. Fall back only after its explicit
// unknown-option response, and only for the read operations used by this
// package. Mutation errors and routing failures never trigger another command.
func compatibleIP(ctx context.Context, binary string, args []string, invoke Runner) ([]byte, error) {
	jsonRequested := false
	for _, arg := range args {
		jsonRequested = jsonRequested || arg == "-j"
	}
	if !jsonRequested {
		return invoke(ctx, binary, args...)
	}
	if _, known := legacyIPBinaries.Load(binary); !known {
		out, err := invoke(ctx, binary, args...)
		if err == nil {
			return out, nil
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return nil, err
		}
		message := strings.ToLower(string(exit.Stderr))
		if !strings.Contains(message, "-j") || !(strings.Contains(message, "unknown") || strings.Contains(message, "invalid option") || strings.Contains(message, "unrecognized option")) {
			return nil, err
		}
		legacyIPBinaries.Store(binary, true)
	}
	plain := []string{"-o"}
	query := []string{}
	for _, arg := range args {
		if arg == "-j" {
			continue
		}
		plain = append(plain, arg)
		if arg != "-4" && arg != "-6" {
			query = append(query, arg)
		}
	}
	if len(query) < 2 || !((query[0] == "rule" && query[1] == "show") || (query[0] == "route" && (query[1] == "show" || query[1] == "get"))) {
		return nil, errors.New("unsupported legacy ip read operation")
	}
	if query[0] == "route" && query[1] == "get" {
		if len(query) != 5 || query[3] != "mark" {
			return nil, errors.New("unsupported legacy marked lookup")
		}
		destination, err := netip.ParseAddr(query[2])
		if err != nil {
			return nil, err
		}
		mark, err := strconv.ParseUint(query[4], 0, 32)
		if err != nil {
			return nil, err
		}
		return lookupMarkedRoute(ctx, destination, uint32(mark))
	}
	out, err := invoke(ctx, binary, plain...)
	if err != nil {
		return nil, err
	}
	return parseLegacyIP(string(out), query[0] == "rule")
}

func numeric(value string) (any, error) {
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid legacy ip number %q", value)
	}
	return n, nil
}
func tableValue(value string) any {
	if n, err := strconv.ParseUint(value, 10, 32); err == nil {
		return n
	}
	return value
}

func parseLegacyIP(output string, rules bool) ([]byte, error) {
	rows := []map[string]any{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.ReplaceAll(line, "\\", " "))
		if len(fields) == 0 {
			continue
		}
		row := map[string]any{}
		if rules {
			if !strings.HasSuffix(fields[0], ":") {
				return nil, errors.New("legacy ip rule lacks priority")
			}
			p, err := numeric(strings.TrimSuffix(fields[0], ":"))
			if err != nil {
				return nil, err
			}
			row["priority"] = p
			fields = fields[1:]
		} else {
			switch fields[0] {
			case "unicast", "unreachable", "prohibit", "blackhole", "throw", "local", "broadcast", "multicast", "anycast", "nat":
				row["type"] = fields[0]
				fields = fields[1:]
			}
			if len(fields) == 0 {
				return nil, errors.New("legacy ip route lacks destination")
			}
			if fields[0] != "default" {
				if _, err := netip.ParseAddr(fields[0]); err != nil {
					if _, err := netip.ParsePrefix(fields[0]); err != nil {
						return nil, errors.New("invalid legacy route destination")
					}
				}
			}
			row["dst"] = fields[0]
			fields = fields[1:]
		}
		for i := 0; i < len(fields); i++ {
			key := fields[i]
			if rules && (key == "not" || key == "l3mdev") {
				row[key] = true
				continue
			}
			if rules && (key == "unreachable" || key == "blackhole" || key == "prohibit") {
				row["action"] = key
				continue
			}
			if !rules {
				switch key {
				case "cache":
					row["cache"] = []string{}
					continue
				case "linkdown", "onlink", "offload", "dead", "pervasive", "notify", "trap":
					flags, _ := row["flags"].([]string)
					row["flags"] = append(flags, key)
					continue
				}
			}
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("legacy ip field %q lacks value", key)
			}
			i++
			value := fields[i]
			if !rules && key == "via" && (value == "inet" || value == "inet6") {
				if i+1 >= len(fields) {
					return nil, errors.New("legacy gateway lacks address")
				}
				i++
				value = fields[i]
			}
			if rules {
				switch key {
				case "from":
					row["src"] = value
				case "lookup", "table":
					row["table"] = tableValue(value)
				case "fwmark":
					parts := strings.Split(value, "/")
					if len(parts) > 2 {
						return nil, errors.New("invalid legacy rule mask")
					}
					mark, e := strconv.ParseUint(parts[0], 0, 32)
					if e != nil {
						return nil, e
					}
					row["fwmark"] = fmt.Sprintf("0x%x", mark)
					if len(parts) == 2 {
						mask, e := strconv.ParseUint(parts[1], 0, 32)
						if e != nil {
							return nil, e
						}
						row["fwmask"] = fmt.Sprintf("0x%x", mask)
					}
				case "to", "iif", "oif", "uidrange", "ipproto", "sport", "dport", "suppress_prefixlength", "suppress_ifgroup", "goto", "nat", "protocol":
					row[key] = value
				default:
					return nil, fmt.Errorf("unsupported legacy rule field %q", key)
				}
			} else {
				switch key {
				case "via":
					if _, e := netip.ParseAddr(value); e != nil {
						return nil, e
					}
					row["gateway"] = value
				case "src":
					if _, e := netip.ParseAddr(value); e != nil {
						return nil, e
					}
					row["prefsrc"] = value
				case "dev", "scope", "pref", "from", "tos", "realm", "realms", "mtu", "advmss", "expires":
					row[key] = value
				case "table":
					row["table"] = tableValue(value)
				case "proto":
					row["protocol"] = tableValue(value)
				case "metric", "uid", "mark":
					n, e := numeric(value)
					if e != nil {
						return nil, e
					}
					row[key] = n
				case "error":
					if _, e := strconv.ParseInt(value, 10, 32); e != nil {
						return nil, e
					}
				default:
					return nil, fmt.Errorf("unsupported legacy route field %q", key)
				}
			}
		}
		rows = append(rows, row)
	}
	return json.Marshal(rows)
}
