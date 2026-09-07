// Package configmerge mirrors sing-box's behavior when started with
// `-C config.d/`: read all *.json files in the directory in
// lexicographic order, concatenate the well-known top-level arrays
// (inbounds, outbounds, dns.servers, dns.rules, route.rules,
// route.rule_set), resolve conflicting scalar sub-keys of `dns` and
// `route` FIRST-FILE-WINS (matching sing-box), and last-writer-wins
// everything else. Subdirectories (e.g. `disabled/`) are ignored — the
// orchestrator parks inactive slots there.
//
// The merge is read-only and lives outside the orchestrator so handlers
// can render a diagnostic preview without holding orchestrator locks.
package configmerge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// fileSource is one slot file we are about to merge.
type fileSource struct {
	path string
	name string // basename for error messages
}

// mergedArrayPaths defines the (parent, key) of every top-level array
// that we concatenate across files. parent="" means root. Anything else
// is treated as last-writer-wins.
var mergedArrayPaths = []struct {
	parent string
	key    string
}{
	{"", "inbounds"},
	{"", "outbounds"},
	{"dns", "servers"},
	{"dns", "rules"},
	{"route", "rules"},
	{"route", "rule_set"},
	{"", "http_clients"},
}

// taggedArrays are the merged paths whose elements carry a unique
// `tag` field. sing-box rejects duplicate tags at startup; we mirror
// that and return CollisionError instead of silently dropping rows.
var taggedArrays = map[string]bool{
	"inbounds":     true,
	"outbounds":    true,
	"servers":      true, // dns.servers
	"rule_set":     true, // route.rule_set
	"http_clients": true,
}

// CollisionError is returned when the same tag appears in two slot
// files within a tag-bearing array. Callers should surface it verbatim
// — the message names the offending tag, kind, and both files.
type CollisionError struct {
	Tag        string
	Kind       string // "inbounds" | "outbounds" | "servers" | "rule_set" | "http_clients"
	FirstFile  string
	SecondFile string
}

func (e *CollisionError) Error() string {
	return fmt.Sprintf("tag collision: %s %q appears in both %s and %s",
		e.Kind, e.Tag, e.FirstFile, e.SecondFile)
}

// MergeDir reads every *.json file directly inside dir (subdirectories
// like `disabled/` are skipped), merges them in lexicographic name order,
// and returns the pretty-printed result.
func MergeDir(dir string) (string, error) {
	sources, err := collectActiveSlots(dir)
	if err != nil {
		return "", err
	}
	merged, err := mergeSources(sources)
	if err != nil {
		return "", err
	}
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal merged: %w", err)
	}
	return string(out), nil
}

func collectActiveSlots(dir string) ([]fileSource, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read config dir %s: %w", dir, err)
	}
	var sources []fileSource
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		sources = append(sources, fileSource{
			path: filepath.Join(dir, e.Name()),
			name: e.Name(),
		})
	}
	sort.Slice(sources, func(i, j int) bool {
		return sources[i].name < sources[j].name
	})
	return sources, nil
}

func mergeSources(sources []fileSource) (map[string]any, error) {
	parsed := make([]map[string]any, 0, len(sources))
	for _, s := range sources {
		data, err := os.ReadFile(s.path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", s.name, err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parse %s: %w", s.name, err)
		}
		parsed = append(parsed, m)
	}

	merged := map[string]any{}

	// Pass 1: last-writer-wins for top-level keys, with shallow merge
	// for nested objects under "dns", "route", "experimental", "log".
	//
	// "dns" and "route" sub-keys resolve FIRST-FILE-WINS to mirror sing-box's
	// real config.d merge: when two slots set the same scalar sub-key (e.g.
	// dns.final, dns.strategy, route.final) sing-box keeps the value from the
	// lexically-first file, NOT the last. The old last-writer-wins here made
	// the merge preview and DNS inspector report the router's dns.final while
	// runtime actually kept base's — the preview lied (bug #445). Arrays under
	// dns/route (servers, rules, rule_set) are overwritten by Pass 2's
	// concatenation, so their Pass-1 value does not matter.
	//
	// "experimental" and "log" keep last-writer-wins — they are not part of
	// the router/base first-wins contract and existing behavior (e.g. a slot
	// raising log.level) depends on the later file winning.
	for _, src := range parsed {
		for k, v := range src {
			switch k {
			case "dns", "route":
				dstObj, _ := merged[k].(map[string]any)
				if dstObj == nil {
					dstObj = map[string]any{}
					merged[k] = dstObj
				}
				if srcObj, ok := v.(map[string]any); ok {
					for sk, sv := range srcObj {
						if _, exists := dstObj[sk]; !exists {
							dstObj[sk] = sv
						}
					}
				}
			case "experimental", "log":
				dstObj, _ := merged[k].(map[string]any)
				if dstObj == nil {
					dstObj = map[string]any{}
					merged[k] = dstObj
				}
				if srcObj, ok := v.(map[string]any); ok {
					for sk, sv := range srcObj {
						dstObj[sk] = sv
					}
				}
			default:
				merged[k] = v
			}
		}
	}

	// Pass 2: replace each merged-array slot with concatenation across
	// all source files; check tag collisions for tagged arrays.
	seen := map[string]string{} // "kind:tag" -> first source name
	for _, ap := range mergedArrayPaths {
		var concat []any
		for i, src := range parsed {
			arr := getArrayAt(src, ap.parent, ap.key)
			if taggedArrays[ap.key] {
				for _, item := range arr {
					obj, _ := item.(map[string]any)
					if obj == nil {
						continue
					}
					tag, _ := obj["tag"].(string)
					if tag == "" {
						continue
					}
					seenKey := ap.key + ":" + tag
					if first, dup := seen[seenKey]; dup {
						return nil, &CollisionError{
							Tag:        tag,
							Kind:       ap.key,
							FirstFile:  first,
							SecondFile: sources[i].name,
						}
					}
					seen[seenKey] = sources[i].name
				}
			}
			concat = append(concat, arr...)
		}
		setArrayAt(merged, ap.parent, ap.key, concat)
	}
	return merged, nil
}

func getArrayAt(m map[string]any, parent, key string) []any {
	if parent == "" {
		v, _ := m[key].([]any)
		return v
	}
	p, _ := m[parent].(map[string]any)
	if p == nil {
		return nil
	}
	v, _ := p[key].([]any)
	return v
}

func setArrayAt(m map[string]any, parent, key string, val []any) {
	if val == nil {
		// Don't introduce an empty key when the array was absent
		// from every source.
		return
	}
	if parent == "" {
		m[key] = val
		return
	}
	p, _ := m[parent].(map[string]any)
	if p == nil {
		p = map[string]any{}
		m[parent] = p
	}
	p[key] = val
}
