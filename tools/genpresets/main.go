// Command genpresets maintains internal/presets/defaults.json: it loads the
// committed catalog as base, refreshes DNS by decompiling each sing-box preset's
// .srs with a host sing-box, and appends new presets from the additions table.
// DEV TOOL — needs network + a host sing-box; never run on the router or in CI.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/hoaxisr/awg-manager/internal/presets"
)

func main() {
	singbox := flag.String("singbox", "sing-box", "path to a host sing-box binary")
	out := flag.String("out", "internal/presets/defaults.json", "catalog path (read as base, rewritten)")
	cacheDir := flag.String("cache", filepath.Join(os.TempDir(), "genpresets-srs"), "srs download cache dir")
	flag.Parse()

	base, err := loadCatalog(*out)
	if err != nil {
		log.Fatalf("load base catalog %s: %v", *out, err)
	}
	if err := os.MkdirAll(*cacheDir, 0o755); err != nil {
		log.Fatalf("cache dir: %v", err)
	}
	dc := newDecompiler(*singbox, *cacheDir)

	catalog := build(base, additions, dc)

	raw, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(*out, append(raw, '\n'), 0o644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}
	log.Printf("wrote %d presets to %s", len(catalog), *out)
}

func loadCatalog(path string) ([]presets.Preset, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ps []presets.Preset
	return ps, json.Unmarshal(raw, &ps)
}

// newDecompiler downloads each .srs (cached by URL hash) and decompiles it via
// the host sing-box, returning DNS-compatible domains+subnets.
func newDecompiler(singbox, cacheDir string) decompiler {
	client := &http.Client{Timeout: 60 * time.Second}
	return func(url string) ([]string, []string, error) {
		srsPath, err := fetchCached(client, pinnedFetchURL(url), cacheDir)
		if err != nil {
			return nil, nil, err
		}
		jsonPath := srsPath + ".json"
		cmd := exec.Command(singbox, "rule-set", "decompile", "--output", jsonPath, srsPath)
		if outp, err := cmd.CombinedOutput(); err != nil {
			return nil, nil, fmt.Errorf("sing-box decompile %s: %v: %s", url, err, outp)
		}
		decompiled, err := os.ReadFile(jsonPath)
		if err != nil {
			return nil, nil, err
		}
		dom, sub, skipped, err := extractRuleSet(decompiled)
		if err != nil {
			return nil, nil, err
		}
		if skipped["domain_keyword"]+skipped["domain_regex"] > 0 {
			log.Printf("note: %s skipped %d keyword + %d regex rules (DNS engine cannot express them)",
				url, skipped["domain_keyword"], skipped["domain_regex"])
		}
		return dom, sub, nil
	}
}

func fetchCached(client *http.Client, url, cacheDir string) (string, error) {
	sum := sha256.Sum256([]byte(url))
	dst := filepath.Join(cacheDir, hex.EncodeToString(sum[:])+".srs")
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", err
	}
	return dst, nil
}
