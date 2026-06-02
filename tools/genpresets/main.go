// Command genpresets regenerates internal/presets/defaults.json from the two
// existing catalogs, reconciling DNS domains by decompiling each .srs with a
// host sing-box. DEV TOOL — needs network + a host sing-box; never run on the
// router and never in CI. See README.md.
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

	ip "github.com/hoaxisr/awg-manager/internal/singbox/router/internalpresets"
)

func main() {
	singbox := flag.String("singbox", "sing-box", "path to a host sing-box binary")
	svcJSON := flag.String("service-presets", "/tmp/service-presets.json", "SERVICE_PRESETS JSON export")
	out := flag.String("out", "internal/presets/defaults.json", "output path")
	cacheDir := flag.String("cache", filepath.Join(os.TempDir(), "genpresets-srs"), "srs download cache dir")
	flag.Parse()

	svc, err := loadServicePresets(*svcJSON)
	if err != nil {
		log.Fatalf("load service presets: %v", err)
	}
	if err := os.MkdirAll(*cacheDir, 0o755); err != nil {
		log.Fatalf("cache dir: %v", err)
	}
	dc := newDecompiler(*singbox, *cacheDir)

	catalog := build(ip.All(), svc, additions, dc)

	raw, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(*out, append(raw, '\n'), 0o644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}
	log.Printf("wrote %d presets to %s", len(catalog), *out)
}

// (main.go does not import internal/presets directly — the catalog's element
// type is inferred from build()'s return value, so no named use is needed.)

func loadServicePresets(path string) ([]servicePreset, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sp []servicePreset
	return sp, json.Unmarshal(raw, &sp)
}

// newDecompiler downloads each .srs (cached by URL hash) and decompiles it via
// the host sing-box, returning DNS-compatible domains+subnets.
func newDecompiler(singbox, cacheDir string) decompiler {
	client := &http.Client{Timeout: 60 * time.Second}
	return func(url string) ([]string, []string, error) {
		srsPath, err := fetchCached(client, url, cacheDir)
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
