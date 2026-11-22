package openflux

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
)

// Prefix starts every openflux share link.
const Prefix = "openflux://v1/"

// maxLinkPayload bounds the decompressed JSON payload of an openflux:// link.
const maxLinkPayload = 16 << 10

// ShareTransport describes one transport in an OpenFlux shareable profile.
type ShareTransport struct {
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	URL      string `json:"url,omitempty"`
	Priority int    `json:"priority,omitempty"`
	Dial     string `json:"dial,omitempty"` // host:port for direct
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
}

// ShareConfig is the JSON payload of an openflux://v1/ link.
type ShareConfig struct {
	Name       string           `json:"name,omitempty"`
	Negotiate  bool             `json:"negotiate,omitempty"`
	Codec      string           `json:"codec,omitempty"`
	Secret     string           `json:"secret,omitempty"`
	Context    string           `json:"context,omitempty"`
	Mode       string           `json:"mode,omitempty"`
	Transports []ShareTransport `json:"transports"`
}

// Encode converts ShareConfig into an openflux://v1/... link.
func Encode(c ShareConfig) (string, error) {
	if len(c.Transports) == 0 {
		return "", fmt.Errorf("share: no transports")
	}
	if len(c.Transports) > 1 && !c.Negotiate {
		return "", fmt.Errorf("share: several transports need a negotiated session")
	}
	if c.Negotiate && SecretChars(c.Secret) < MinSecretChars {
		return "", fmt.Errorf("share: a negotiated session needs a secret of at least %d characters", MinSecretChars)
	}

	raw, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("failed to marshal share config: %w", err)
	}

	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		return "", fmt.Errorf("failed to create flate writer: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return "", fmt.Errorf("failed to compress share config: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("failed to close flate writer: %w", err)
	}

	return Prefix + base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

func normalizeLinkBody(b string) string {
	b = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n', '\u00a0', '\u200b':
			return -1
		case '+':
			return '-'
		case '/':
			return '_'
		}
		return r
	}, b)
	return strings.TrimRight(b, "=")
}

// Decode parses and validates an openflux://v1/... link into ShareConfig.
func Decode(link string) (ShareConfig, error) {
	link = strings.TrimSpace(link)
	if !strings.HasPrefix(link, Prefix) {
		if strings.HasPrefix(strings.ToLower(link), "openflux://") && !strings.HasPrefix(link, "openflux://") {
			return ShareConfig{}, fmt.Errorf("регистр символов в ссылке изменён (ссылки openflux:// чувствительны к регистру); скопируйте её заново")
		}
		if strings.HasPrefix(link, "openflux://") {
			return ShareConfig{}, fmt.Errorf("неподдерживаемая версия ссылки openflux:// (ожидается %s)", Prefix)
		}
		return ShareConfig{}, fmt.Errorf("некорректная ссылка: должна начинаться с %s", Prefix)
	}

	body := normalizeLinkBody(strings.TrimPrefix(link, Prefix))
	if body == "" {
		return ShareConfig{}, fmt.Errorf("ссылка повреждена: пустое тело после %s", Prefix)
	}

	packed, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return ShareConfig{}, fmt.Errorf("ссылка повреждена (ошибка кодирования base64url): %w", err)
	}

	raw, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(packed)), maxLinkPayload+1))
	if err != nil {
		return ShareConfig{}, fmt.Errorf("ссылка повреждена (ошибка распаковки DEFLATE): %w", err)
	}
	if len(raw) > maxLinkPayload {
		return ShareConfig{}, fmt.Errorf("слишком большой размер данных в ссылке")
	}

	var sc ShareConfig
	if err := json.Unmarshal(raw, &sc); err != nil {
		return ShareConfig{}, fmt.Errorf("некорректный JSON-профиль в ссылке: %w", err)
	}

	if len(sc.Transports) == 0 {
		return ShareConfig{}, fmt.Errorf("в ссылке отсутствуют транспорты")
	}
	if sc.Negotiate && SecretChars(sc.Secret) < MinSecretChars {
		return ShareConfig{}, fmt.Errorf("в сессионной ссылке ключ шифрования короче %d символов", MinSecretChars)
	}
	if sc.Secret != "" && SecretChars(sc.Secret) < MinSecretChars {
		return ShareConfig{}, fmt.Errorf("ключ шифрования в ссылке короче %d символов", MinSecretChars)
	}

	return sc, nil
}

// ClientConfigFromShare converts a decoded ShareConfig into a ClientConfig while preserving local listen settings from base.
func ClientConfigFromShare(sc ShareConfig, base ClientConfig) ClientConfig {
	out := base
	if out.Socks5Listen == "" {
		out.Socks5Listen = fmt.Sprintf("127.0.0.1:%d", DefaultClientSocks5Port)
	}
	if strings.TrimSpace(sc.Name) != "" {
		out.Name = strings.TrimSpace(sc.Name)
	} else if strings.TrimSpace(out.Name) == "" {
		out.Name = "OpenFlux Выход"
	}

	if strings.EqualFold(strings.TrimSpace(sc.Mode), "stream") {
		out.TransportMode = "stream"
	} else if sc.Negotiate || len(sc.Transports) > 1 {
		out.TransportMode = "session"
	} else {
		out.TransportMode = "single"
	}

	if strings.EqualFold(strings.TrimSpace(sc.Codec), "legacy") {
		out.Codec = "legacy"
	} else {
		out.Codec = "batched"
	}

	out.Secret = strings.TrimSpace(sc.Secret)

	// Derive KDF context if not explicitly provided in link.
	if strings.TrimSpace(sc.Context) != "" {
		out.Context = strings.TrimSpace(sc.Context)
	} else {
		bestPrio := -1
		bestURL := ""
		for _, t := range sc.Transports {
			if u := strings.TrimSpace(t.URL); u != "" {
				if bestURL == "" || t.Priority > bestPrio {
					bestPrio = t.Priority
					bestURL = u
				}
			}
		}
		if bestURL != "" {
			out.Context = bestURL
		} else {
			out.Context = "http://#"
		}
	}

	out.Transports = nil
	out.DirectDial = ""
	out.WebdavURL = ""
	out.WebdavUser = ""
	out.WebdavPassword = ""
	out.MailruURL = ""
	out.YandexURL = ""
	out.VyandexURL = ""
	out.BoardsURL = ""
	out.AvitoURL = ""
	out.CupsonlineURL = ""
	out.TelemostURL = ""

	seen := make(map[string]bool)
	for _, t := range sc.Transports {
		norm := strings.ToLower(strings.TrimSpace(t.Type))
		if norm == "" {
			continue
		}
		if !seen[norm] {
			seen[norm] = true
			out.Transports = append(out.Transports, norm)
		}
		switch norm {
		case "direct":
			if strings.TrimSpace(t.Dial) != "" {
				out.DirectDial = strings.TrimSpace(t.Dial)
			}
		case "webdav":
			if strings.TrimSpace(t.URL) != "" {
				out.WebdavURL, out.WebdavUser, out.WebdavPassword = parseWebdavURL(t.URL, t.User, t.Password)
			}
		case "mailru":
			if u := NormalizeMultiURL(t.URL); u != "" {
				out.MailruURL = u
			}
		case "vyandex":
			if u := NormalizeMultiURL(t.URL); u != "" {
				out.VyandexURL = u
				if out.YandexURL == "" {
					out.YandexURL = u
				}
			}
		case "yandex":
			if u := NormalizeMultiURL(t.URL); u != "" {
				out.YandexURL = u
			}
		case "boards":
			if u := NormalizeMultiURL(t.URL); u != "" {
				out.BoardsURL = u
			}
		case "avito":
			if u := NormalizeMultiURL(t.URL); u != "" {
				out.AvitoURL = u
			}
		case "cupsonline":
			if u := NormalizeMultiURL(t.URL); u != "" {
				out.CupsonlineURL = u
			}
		case "telemost":
			if u := NormalizeMultiURL(t.URL); u != "" {
				out.TelemostURL = u
			}
		}
	}

	return out
}

// GenerateLink constructs an openflux:// link from the server configuration and given public host/IP.
func GenerateLink(cfg Config, publicHost string) (string, error) {
	isSession := cfg.TransportMode == "session" || cfg.TransportMode == "" || len(cfg.Transports) > 1

	sc := ShareConfig{
		Name:      cfg.Name,
		Negotiate: isSession,
		Secret:    strings.TrimSpace(cfg.Secret),
	}
	if sc.Name == "" {
		sc.Name = "OpenFlux Server"
	}
	if strings.ToLower(strings.TrimSpace(cfg.Codec)) == "legacy" {
		sc.Codec = "legacy"
	}

	if strings.TrimSpace(cfg.WebdavURL) != "" {
		sc.Context = sanitizeContextURL(cfg.WebdavURL)
	} else if u := NormalizeMultiURL(cfg.MailruURL); u != "" {
		sc.Context = u
	} else if u := NormalizeMultiURL(cfg.VyandexURL); u != "" {
		sc.Context = u
	} else if u := NormalizeMultiURL(cfg.YandexURL); u != "" {
		sc.Context = u
	} else if u := NormalizeMultiURL(cfg.BoardsURL); u != "" {
		sc.Context = u
	} else if u := NormalizeMultiURL(cfg.AvitoURL); u != "" {
		sc.Context = u
	} else if u := NormalizeMultiURL(cfg.CupsonlineURL); u != "" {
		sc.Context = u
	} else {
		sc.Context = "http://#"
	}

	dialHost := strings.TrimSpace(publicHost)
	if strings.TrimSpace(cfg.DirectHost) != "" {
		dialHost = strings.TrimSpace(cfg.DirectHost)
	}
	directPort := cfg.DirectPort()
	if dialHost != "" {
		if _, _, err := net.SplitHostPort(dialHost); err != nil {
			dialHost = net.JoinHostPort(dialHost, strconv.Itoa(directPort))
		}
	}

	if isSession {
		for _, t := range cfg.Transports {
			norm := strings.ToLower(strings.TrimSpace(t))
			prio := DefaultTransportPriority(norm)
			if cfg.TransportPriorities != nil {
				if custom, ok := cfg.TransportPriorities[norm]; ok && custom > 0 {
					prio = custom
				}
			}
			switch norm {
			case "webdav":
				if strings.TrimSpace(cfg.WebdavURL) != "" {
					webdavURL, webdavUser, webdavPass := parseWebdavURL(cfg.WebdavURL, cfg.WebdavUser, cfg.WebdavPassword)
					sc.Transports = append(sc.Transports, ShareTransport{
						Type:     "webdav",
						Priority: prio,
						URL:      webdavURL,
						User:     webdavUser,
						Password: webdavPass,
					})
				}
			case "direct":
				if dialHost != "" {
					sc.Transports = append(sc.Transports, ShareTransport{
						Type:     "direct",
						Priority: prio,
						Dial:     dialHost,
					})
				}
			case "mailru":
				if u := NormalizeMultiURL(cfg.MailruURL); u != "" {
					sc.Transports = append(sc.Transports, ShareTransport{
						Type:     "mailru",
						Priority: prio,
						URL:      u,
					})
				}
			case "vyandex":
				vyURL := NormalizeMultiURL(cfg.VyandexURL)
				if vyURL == "" {
					vyURL = NormalizeMultiURL(cfg.YandexURL)
				}
				if vyURL != "" {
					sc.Transports = append(sc.Transports, ShareTransport{
						Type:     "vyandex",
						Priority: prio,
						URL:      vyURL,
					})
				}
			case "yandex":
				if u := NormalizeMultiURL(cfg.YandexURL); u != "" {
					sc.Transports = append(sc.Transports, ShareTransport{
						Type:     "yandex",
						Priority: prio,
						URL:      u,
					})
				}
			case "boards":
				if u := NormalizeMultiURL(cfg.BoardsURL); u != "" {
					sc.Transports = append(sc.Transports, ShareTransport{
						Type:     "boards",
						Priority: prio,
						URL:      u,
					})
				}
			case "avito":
				if u := NormalizeMultiURL(cfg.AvitoURL); u != "" {
					sc.Transports = append(sc.Transports, ShareTransport{
						Type:     "avito",
						Priority: prio,
						URL:      u,
					})
				}
			case "cupsonline":
				if u := NormalizeMultiURL(cfg.CupsonlineURL); u != "" {
					sc.Transports = append(sc.Transports, ShareTransport{
						Type:     "cupsonline",
						Priority: prio,
						URL:      u,
					})
				}
			case "telemost":
				if u := NormalizeMultiURL(cfg.TelemostURL); u != "" {
					sc.Transports = append(sc.Transports, ShareTransport{
						Type:     "telemost",
						Priority: prio,
						URL:      u,
					})
				}
			}
		}

		sort.SliceStable(sc.Transports, func(i, j int) bool {
			return sc.Transports[i].Priority > sc.Transports[j].Priority
		})
	} else {
		single := ""
		if len(cfg.Transports) > 0 {
			single = strings.ToLower(strings.TrimSpace(cfg.Transports[0]))
		}
		switch single {
		case "webdav":
			if strings.TrimSpace(cfg.WebdavURL) != "" {
				webdavURL, webdavUser, webdavPass := parseWebdavURL(cfg.WebdavURL, cfg.WebdavUser, cfg.WebdavPassword)
				sc.Transports = append(sc.Transports, ShareTransport{
					Type:     "webdav",
					URL:      webdavURL,
					User:     webdavUser,
					Password: webdavPass,
				})
			}
		case "direct":
			if dialHost != "" {
				sc.Transports = append(sc.Transports, ShareTransport{
					Type: "direct",
					Dial: dialHost,
				})
			}
		case "mailru":
			if u := NormalizeMultiURL(cfg.MailruURL); u != "" {
				sc.Transports = append(sc.Transports, ShareTransport{
					Type: "mailru",
					URL:  u,
				})
			}
		case "vyandex":
			vyURL := NormalizeMultiURL(cfg.VyandexURL)
			if vyURL == "" {
				vyURL = NormalizeMultiURL(cfg.YandexURL)
			}
			if vyURL != "" {
				sc.Transports = append(sc.Transports, ShareTransport{
					Type: "vyandex",
					URL:  vyURL,
				})
			}
		case "yandex":
			if u := NormalizeMultiURL(cfg.YandexURL); u != "" {
				sc.Transports = append(sc.Transports, ShareTransport{
					Type: "yandex",
					URL:  u,
				})
			}
		case "boards":
			if u := NormalizeMultiURL(cfg.BoardsURL); u != "" {
				sc.Transports = append(sc.Transports, ShareTransport{
					Type: "boards",
					URL:  u,
				})
			}
		case "avito":
			if u := NormalizeMultiURL(cfg.AvitoURL); u != "" {
				sc.Transports = append(sc.Transports, ShareTransport{
					Type: "avito",
					URL:  u,
				})
			}
		case "cupsonline":
			if u := NormalizeMultiURL(cfg.CupsonlineURL); u != "" {
				sc.Transports = append(sc.Transports, ShareTransport{
					Type: "cupsonline",
					URL:  u,
				})
			}
		case "telemost":
			if u := NormalizeMultiURL(cfg.TelemostURL); u != "" {
				sc.Transports = append(sc.Transports, ShareTransport{
					Type: "telemost",
					URL:  u,
				})
			}
		}
	}

	if len(sc.Transports) == 0 {
		return "", fmt.Errorf("нет доступных транспортов для ссылки (укажите URL облачного транспорта или адрес сервера direct)")
	}

	return Encode(sc)
}
