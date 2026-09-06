package obfuscator

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// MaxPackageBytes — в пакете пять бинарей; лимит на архив (Q25).
const MaxPackageBytes = 16 << 20

const packageName = "package.tar.gz"

// PackageURL: из ссылки установки (…/api/install/<token>) или прямой ссылки
// на пакет — URL пакета.
func PackageURL(installURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(installURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("ожидается http(s)-ссылка установки Phobos")
	}
	p := strings.TrimSuffix(u.Path, "/")
	if path.Base(p) == packageName {
		p = path.Dir(p)
	}
	if !strings.Contains(p, "/api/install/") || path.Base(p) == "install" {
		return "", errors.New("это не ссылка установки Phobos (…/api/install/<token>)")
	}
	u.Path, u.RawQuery, u.Fragment = p+"/"+packageName, "", ""
	return u.String(), nil
}

// insecureClient: панель Phobos обычно на самоподписанном TLS, их скрипт
// качает с curl -k. Проверку не делаем (Q14), UI об этом предупреждает.
var insecureClient = &http.Client{
	Timeout: 60 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // Q14: осознанно
		ForceAttemptHTTP2: false,
	},
}

// FetchPhobosConf качает пакет и достаёт из него ТОЛЬКО клиентский .conf
// (не wg-obfuscator.conf / wg-socks5-obfuscator.conf). Бинари из пакета
// не трогаем (Q15b).
func FetchPhobosConf(ctx context.Context, installURL string) (string, error) {
	pkgURL, err := PackageURL(installURL)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pkgURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := insecureClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("панель Phobos: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("панель Phobos ответила %s (ссылка истекла?)", resp.Status)
	}
	if resp.ContentLength > MaxPackageBytes {
		return "", fmt.Errorf("пакет слишком большой (%d байт, лимит %d)", resp.ContentLength, MaxPackageBytes)
	}
	conf, err := extractClientConf(&cappedReader{r: resp.Body, left: MaxPackageBytes})
	if errors.Is(err, errPackageTooBig) {
		return "", fmt.Errorf("пакет слишком большой (лимит %d байт)", MaxPackageBytes)
	}
	return conf, err
}

var errPackageTooBig = errors.New("package too big")

// cappedReader — как io.LimitReader, но с внятной ошибкой вместо «unexpected EOF»
// у gzip/tar, когда Content-Length не прислан.
type cappedReader struct {
	r    io.Reader
	left int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errPackageTooBig
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

func extractClientConf(r io.Reader) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", fmt.Errorf("пакет не gzip: %w", err)
	}
	// Разжимаем целиком (в пределах лимита) до разбора tar: иначе gzip-бомба
	// с мусорным содержимым уронит tar раньше, чем сработает cappedReader.
	data, err := io.ReadAll(&cappedReader{r: gz, left: MaxPackageBytes})
	if err != nil {
		if errors.Is(err, errPackageTooBig) {
			return "", err
		}
		return "", fmt.Errorf("пакет: %w", err)
	}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", errors.New("в пакете нет клиентского .conf")
		}
		if err != nil {
			return "", fmt.Errorf("пакет: %w", err)
		}
		base := path.Base(h.Name)
		if h.Typeflag != tar.TypeReg || !strings.HasSuffix(base, ".conf") || strings.Contains(base, "obfuscator") {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, 64<<10))
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}
