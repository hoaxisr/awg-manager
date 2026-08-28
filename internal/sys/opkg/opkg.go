package opkg

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const (
	opkgBin      = "/opt/bin/opkg"
	statusFile   = "/opt/lib/opkg/status"
	listCacheTTL = 10 * time.Minute
	maxListScan  = 20000
)

var (
	installedLine  = regexp.MustCompile(`^(.+?) - (.+)$`)
	upgradableLine = regexp.MustCompile(`^(.+?) - (.+?) - (.+)$`)
)

// Package is one opkg package entry.
type Package struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	UpgradeVersion string `json:"upgradeVersion,omitempty"`
	Description    string `json:"description,omitempty"`
	InstalledAt    string `json:"installedAt,omitempty"`
}

// Client wraps opkg CLI on Entware.
type Client struct {
	Bin string

	mu         sync.Mutex
	listCache  []Package
	listCached time.Time
}

func NewClient() *Client {
	return &Client{Bin: opkgBin}
}

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return opkgBin
}

func (c *Client) available() error {
	if _, err := os.Stat(c.bin()); err != nil {
		return fmt.Errorf("opkg not found at %s (install Entware)", c.bin())
	}
	return nil
}

func (c *Client) run(args ...string) (stdout string, exitCode int, err error) {
	if err := c.available(); err != nil {
		return "", 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, runErr := exec.Run(ctx, c.bin(), args...)
	stdout = strings.TrimSpace(result.Stdout)
	if stdout == "" {
		stdout = strings.TrimSpace(result.Stderr)
	}
	exitCode = 0
	if result != nil {
		exitCode = result.ExitCode
	}
	if runErr != nil {
		return stdout, exitCode, runErr
	}
	return stdout, exitCode, nil
}

// ErrLocked — база opkg занята другим процессом (ручной opkg по SSH,
// автообновление). Своим мьютексом это не ловится: он сериализует только
// наши вызовы.
var ErrLocked = errors.New("opkg занят другим процессом: дождитесь окончания его работы")

// ErrBusy — работает наша же долгая операция (update/upgrade/install).
// Чтения не встают в её очередь: ожидание до пяти минут неотличимо от
// зависшей страницы, а отказ виден сразу.
var ErrBusy = errors.New("выполняется другая операция opkg: дождитесь её окончания")

// lockedByOther распознаёт занятую базу по сообщению opkg.
func lockedByOther(out string) bool {
	low := strings.ToLower(out)
	return strings.Contains(low, "could not lock") ||
		strings.Contains(low, "resource temporarily unavailable") ||
		strings.Contains(low, "opkg_conf_load")
}

func (c *Client) runLocked(args ...string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runHeld(args...)
}

// runLockedRead — то же, но без ожидания: занятый клиент отвечает ErrBusy.
func (c *Client) runLockedRead(args ...string) (string, error) {
	if !c.mu.TryLock() {
		return "", ErrBusy
	}
	defer c.mu.Unlock()
	return c.runHeld(args...)
}

// runHeld выполняет opkg; вызывается с уже взятым мьютексом.
func (c *Client) runHeld(args ...string) (string, error) {
	out, code, err := c.run(args...)
	if err != nil {
		if lockedByOther(out) {
			return "", ErrLocked
		}
		if out != "" && isBenignEmptyList(args, out, code) {
			return out, nil
		}
		if out != "" {
			return out, fmt.Errorf("%w: %s", err, out)
		}
		return "", err
	}
	return out, nil
}

func isBenignEmptyList(args []string, out string, code int) bool {
	if code == 0 || strings.TrimSpace(out) != "" {
		return false
	}
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "list-upgradable", "list-installed":
		return true
	default:
		return false
	}
}

// ListInstalled returns installed packages enriched from opkg status.
func (c *Client) ListInstalled() ([]Package, error) {
	text, err := c.runLockedRead("list-installed")
	if err != nil {
		return nil, err
	}
	pkgs := parseInstalled(text)
	meta := readStatusMeta()
	for i := range pkgs {
		if m, ok := meta[pkgs[i].Name]; ok {
			if pkgs[i].Description == "" {
				pkgs[i].Description = m.Description
			}
			pkgs[i].InstalledAt = m.InstalledAt
		}
	}
	return pkgs, nil
}

// ListUpgradable returns packages with available upgrades.
func (c *Client) ListUpgradable() ([]Package, error) {
	text, err := c.runLockedRead("list-upgradable")
	if err != nil {
		return nil, err
	}
	return parseUpgradable(text), nil
}

// ListAvailable returns installable packages (from opkg list) with optional filter and pagination.
func (c *Client) ListAvailable(query string, offset, limit int) (items []Package, total int, err error) {
	all, err := c.allAvailable()
	if err != nil {
		return nil, 0, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	filtered := all
	if q != "" {
		filtered = make([]Package, 0)
		for _, p := range all {
			if strings.Contains(strings.ToLower(p.Name), q) ||
				strings.Contains(strings.ToLower(p.Description), q) {
				filtered = append(filtered, p)
			}
		}
	}
	total = len(filtered)
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 100
	}
	if offset > len(filtered) {
		return []Package{}, total, nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end], total, nil
}

func (c *Client) allAvailable() ([]Package, error) {
	if !c.mu.TryLock() {
		return nil, ErrBusy
	}
	defer c.mu.Unlock()
	if len(c.listCache) > 0 && time.Since(c.listCached) < listCacheTTL {
		return append([]Package{}, c.listCache...), nil
	}
	text, _, err := c.run("list")
	if err != nil && strings.TrimSpace(text) == "" {
		return nil, err
	}
	pkgs := parseList(text)
	c.listCache = pkgs
	c.listCached = time.Now()
	return append([]Package{}, pkgs...), nil
}

// Search finds installable packages by query (opkg find).
func (c *Client) Search(query string) ([]Package, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query required")
	}
	// Поиск уходит в exec тем же путём, что install/remove: без проверки
	// строка вида "-f /tmp/my.conf" читается opkg как флаг.
	if err := validatePkgNames([]string{query}); err != nil {
		return nil, err
	}
	text, err := c.runLockedRead("find", query)
	if err != nil {
		return nil, err
	}
	return parseInstalled(text), nil
}

// Update refreshes package lists (opkg update).
func (c *Client) Update() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listCache = nil
	out, _, err := c.run("update")
	if err != nil {
		if out != "" {
			return out, fmt.Errorf("%w: %s", err, out)
		}
		return "", err
	}
	return out, nil
}

// Upgrade upgrades all upgradable packages.
func (c *Client) Upgrade() (string, error) {
	out, err := c.runLocked("upgrade")
	if err == nil {
		c.mu.Lock()
		c.listCache = nil
		c.mu.Unlock()
	}
	return out, err
}

// UpgradePackages upgrades named packages.
func (c *Client) UpgradePackages(names []string) (string, error) {
	if len(names) == 0 {
		return "", fmt.Errorf("no packages specified")
	}
	if err := validatePkgNames(names); err != nil {
		return "", err
	}
	args := append([]string{"upgrade"}, names...)
	out, err := c.runLocked(args...)
	if err == nil {
		c.mu.Lock()
		c.listCache = nil
		c.mu.Unlock()
	}
	return out, err
}

// Install installs packages by name.
func (c *Client) Install(names []string) (string, error) {
	if len(names) == 0 {
		return "", fmt.Errorf("no packages specified")
	}
	if err := validatePkgNames(names); err != nil {
		return "", err
	}
	args := append([]string{"install"}, names...)
	out, err := c.runLocked(args...)
	if err == nil {
		c.mu.Lock()
		c.listCache = nil
		c.mu.Unlock()
	}
	return out, err
}

// Remove uninstalls packages by name.
func (c *Client) Remove(names []string) (string, error) {
	if len(names) == 0 {
		return "", fmt.Errorf("no packages specified")
	}
	if err := validatePkgNames(names); err != nil {
		return "", err
	}
	args := append([]string{"remove"}, names...)
	out, err := c.runLocked(args...)
	if err == nil {
		c.mu.Lock()
		c.listCache = nil
		c.mu.Unlock()
	}
	return out, err
}

type statusMeta struct {
	Description string
	InstalledAt string
}

func readStatusMeta() map[string]statusMeta {
	out := make(map[string]statusMeta)
	b, err := os.ReadFile(statusFile)
	if err != nil {
		return out
	}
	var (
		name        string
		desc        string
		installedTS string
	)
	flush := func() {
		if name == "" {
			return
		}
		out[name] = statusMeta{Description: desc, InstalledAt: installedTS}
		name, desc, installedTS = "", "", ""
	}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "Package: ") {
			flush()
			name = strings.TrimPrefix(line, "Package: ")
			continue
		}
		if strings.HasPrefix(line, "Description: ") {
			desc = strings.TrimPrefix(line, "Description: ")
			continue
		}
		if strings.HasPrefix(line, "Installed-Time: ") {
			raw := strings.TrimPrefix(line, "Installed-Time: ")
			if sec, err := strconv.ParseInt(raw, 10, 64); err == nil && sec > 0 {
				installedTS = time.Unix(sec, 0).Format("2006-01-02 15:04")
			}
		}
	}
	flush()
	return out
}

func parseList(text string) []Package {
	out := []Package{}
	sc := bufio.NewScanner(strings.NewReader(text))
	n := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " - ", 3)
		if len(parts) < 2 {
			continue
		}
		p := Package{Name: parts[0], Version: parts[1]}
		if len(parts) == 3 {
			p.Description = parts[2]
		}
		out = append(out, p)
		n++
		if n >= maxListScan {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func parseInstalled(text string) []Package {
	out := []Package{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		m := installedLine.FindStringSubmatch(line)
		if len(m) != 3 {
			continue
		}
		out = append(out, Package{Name: m[1], Version: m[2]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func parseUpgradable(text string) []Package {
	out := []Package{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		m := upgradableLine.FindStringSubmatch(line)
		if len(m) != 4 {
			continue
		}
		out = append(out, Package{
			Name:           m[1],
			Version:        m[2],
			UpgradeVersion: m[3],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// pkgNameRegex enforces a safe opkg package name format.
// Leading hyphen is forbidden because opkg treats "-name" as a CLI flag.
var pkgNameRegex = regexp.MustCompile(`^[a-zA-Z0-9._+][a-zA-Z0-9.\-_+]*$`)

func validatePkgNames(names []string) error {
	for _, name := range names {
		if !pkgNameRegex.MatchString(name) {
			return fmt.Errorf("invalid package name format: %q", name)
		}
	}
	return nil
}
