// internal/singbox/migrate_rulesets_fork.go
package singbox

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// MigrateRuleSetURLsToFork rewrites snapshotted vernette rule-set URLs to the
// repo.hoaxisr.ru mirror in every persisted sing-box slot file, so sing-box
// re-fetches the mirror's .srs without the user re-applying presets
// (raw.githubusercontent.com заблокирован у части провайдеров — #534).
// Rule-set URLs live in more than one slot (21-routing.json AND the mode slot
// 20-fakeip.json at least; на немигрированной установке — ещё и файлы прежней
// раскладки), so we sweep ALL *.json across active, disabled/ and pending/
// rather than naming files.
//
// Byte-level replace (not a config round-trip): the pattern is anchored on the
// JSON string-opening quote, so ONLY values that START with the vernette URL
// are rewritten — a user's proxy-wrapped workaround URL (e.g.
// "https://ghproxy.com/https://github.com/vernette/...") is left untouched
// (замена сделала бы его нерабочим гибридом). Preserves any fields the structs
// do not model; files without the pattern are left byte-for-byte untouched.
// Idempotent — after the swap no anchored vernette prefix remains.
//
// Returns changed=true when at least one file was rewritten — caller reloads a
// surviving sing-box so it re-fetches from the mirror without waiting for an
// unrelated reload.
func MigrateRuleSetURLsToFork(configDir string) (bool, error) {
	changed := false
	for _, pat := range []string{
		filepath.Join(configDir, "*.json"),
		filepath.Join(configDir, "disabled", "*.json"),
		filepath.Join(configDir, "pending", "*.json"),
	} {
		matches, err := filepath.Glob(pat)
		if err != nil {
			return changed, fmt.Errorf("glob %s: %w", pat, err)
		}
		for _, p := range matches {
			fileChanged, err := rewriteRuleSetForkURLs(p)
			if err != nil {
				return changed, err
			}
			changed = changed || fileChanged
		}
	}
	return changed, nil
}

func rewriteRuleSetForkURLs(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	replaced := bytes.ReplaceAll(data,
		[]byte(`"https://github.com/vernette/rulesets/raw/master/`),
		[]byte(`"https://repo.hoaxisr.ru/rulesets/`))
	if bytes.Equal(replaced, data) {
		return false, nil
	}
	if err := storage.AtomicWrite(path, replaced); err != nil {
		return false, err
	}
	return true, nil
}
