package freeturn

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	githubLatestReleaseURL = "https://api.github.com/repos/samosvalishe/free-turn-proxy/releases/latest"
	remoteCacheTTL         = 6 * time.Hour
	remoteCheckTimeout     = 10 * time.Second
)

// archAssetBasenames maps awg-manager build arch → upstream release asset names.
var archAssetBasenames = map[string]struct{ Client, Server string }{
	"aarch64-3.10": {Client: "client-linux-arm64", Server: "server-linux-arm64"},
	"mipsel-3.4":   {Client: "client-linux-mipsle-softfloat", Server: "server-linux-mipsle-softfloat"},
	"mips-3.4":     {Client: "client-linux-mips-softfloat", Server: "server-linux-mips-softfloat"},
}

type remoteReleaseCache struct {
	Version   string
	Client    BinarySpec
	Server    BinarySpec
	CheckedAt time.Time
	Err       string
}

// SetArchKey wires the router arch key (detectArch(), e.g. "aarch64-3.10").
func (s *Service) SetArchKey(arch string) {
	s.archKey = arch
}

func (s *Service) refreshRemote(ctx context.Context, force bool) {
	if s.archKey == "" || s.downloader == nil {
		return
	}
	s.remoteMu.Lock()
	if !force && s.remoteCache != nil && time.Since(s.remoteCache.CheckedAt) < remoteCacheTTL {
		s.remoteMu.Unlock()
		return
	}
	s.remoteMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, remoteCheckTimeout)
	defer cancel()

	cache := &remoteReleaseCache{CheckedAt: time.Now()}
	specs, err := fetchGitHubLatest(ctx, s.archKey, s.httpGet)
	if err != nil {
		cache.Err = err.Error()
	} else {
		cache.Version = specs.Client.Version
		cache.Client = specs.Client
		cache.Server = specs.Server
	}

	s.remoteMu.Lock()
	s.remoteCache = cache
	s.remoteMu.Unlock()
}

func (s *Service) remoteStatus() (version, errMsg string) {
	s.remoteMu.RLock()
	defer s.remoteMu.RUnlock()
	if s.remoteCache == nil {
		return "", ""
	}
	return s.remoteCache.Version, s.remoteCache.Err
}

// resolveInstallSpecs returns the build-pinned specs. A remote GitHub release,
// even if newer, is surfaced as information only (Status.RemoteVersion) and is
// never substituted here: the default install path always installs the pin.
// Установка апстрим-сборки должна быть отдельным явным действием пользователя.
func (s *Service) resolveInstallSpecs() (ArchSpecs, string) {
	if s.installSpecs == nil {
		return ArchSpecs{}, ""
	}
	pinned := *s.installSpecs
	return pinned, pinned.Client.Version
}

type httpGetFunc func(ctx context.Context, url string, maxBytes int64) ([]byte, error)

func (s *Service) httpGet(ctx context.Context, url string, maxBytes int64) ([]byte, error) {
	if s.downloader == nil {
		return nil, fmt.Errorf("downloader not configured")
	}
	tmp := s.clientBin + ".remote.tmp"
	if err := s.downloader.DownloadFile(ctx, url, tmp, maxBytes); err != nil {
		return nil, err
	}
	data, err := readFileLimited(tmp, maxBytes)
	_ = os.Remove(tmp)
	return data, err
}

func readFileLimited(path string, max int64) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if max > 0 && int64(len(data)) > max {
		return nil, fmt.Errorf("file too large")
	}
	return data, nil
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

func fetchGitHubLatest(ctx context.Context, archKey string, get httpGetFunc) (ArchSpecs, error) {
	names, ok := archAssetBasenames[archKey]
	if !ok {
		return ArchSpecs{}, fmt.Errorf("неизвестная архитектура %q", archKey)
	}

	body, err := get(ctx, githubLatestReleaseURL, 512*1024)
	if err != nil {
		return ArchSpecs{}, fmt.Errorf("GitHub releases: %w", err)
	}
	var rel ghRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return ArchSpecs{}, fmt.Errorf("GitHub releases JSON: %w", err)
	}
	version := strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
	if version == "" {
		return ArchSpecs{}, fmt.Errorf("GitHub releases: пустой tag")
	}

	var clientURL, serverURL, checksumsURL string
	var clientSize, serverSize int64
	for _, a := range rel.Assets {
		switch a.Name {
		case names.Client:
			clientURL, clientSize = a.BrowserDownloadURL, a.Size
		case names.Server:
			serverURL, serverSize = a.BrowserDownloadURL, a.Size
		case "checksums.txt":
			checksumsURL = a.BrowserDownloadURL
		}
	}
	if clientURL == "" || serverURL == "" {
		return ArchSpecs{}, fmt.Errorf("GitHub release %s: нет бинарей для %s", version, archKey)
	}
	if checksumsURL == "" {
		return ArchSpecs{}, fmt.Errorf("GitHub release %s: нет checksums.txt", version)
	}

	sumBody, err := get(ctx, checksumsURL, 256*1024)
	if err != nil {
		return ArchSpecs{}, fmt.Errorf("checksums.txt: %w", err)
	}
	clientSHA, err := sha256FromChecksums(sumBody, names.Client)
	if err != nil {
		return ArchSpecs{}, err
	}
	serverSHA, err := sha256FromChecksums(sumBody, names.Server)
	if err != nil {
		return ArchSpecs{}, err
	}

	return ArchSpecs{
		Client: BinarySpec{Version: version, URL: clientURL, SHA256: clientSHA, Size: clientSize},
		Server: BinarySpec{Version: version, URL: serverURL, SHA256: serverSHA, Size: serverSize},
	}, nil
}

func sha256FromChecksums(data []byte, assetName string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		name := parts[len(parts)-1]
		if name == assetName || strings.HasSuffix(name, "/"+assetName) {
			return strings.ToLower(parts[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt: нет записи для %s", assetName)
}
