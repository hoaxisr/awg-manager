package warpscout

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxImportBytes = 64 << 10

type importedFields struct {
	id            string
	token         string
	privateKey    string
	peerPublicKey string
	addressV4     string
	addressV6     string
	clientID      string
	reserved      []int
	license       string
	accountType   string
	warpPlus      bool
	hasWarpPlus   bool
	quota         int64
	premiumData   int64
}

// ParseAccountImport распознаёт и проверяет учётную запись Cloudflare WARP
// из warpscout-account.json, ответа API /reg, wgcf-account.toml или .conf
// (WireGuard / AmneziaWG). При отсутствии id/token возвращает пригодный для
// сканера и создания туннеля офлайн-аккаунт (HasToken=false).
func ParseAccountImport(raw string) (*Account, error) {
	if len(raw) > maxImportBytes {
		return nil, fmt.Errorf("%w: размер данных превышает 64 КБ", ErrInvalidInput)
	}
	trimmed := strings.TrimSpace(strings.TrimPrefix(raw, "\xef\xbb\xbf"))
	if trimmed == "" {
		return nil, fmt.Errorf("%w: вставьте данные аккаунта или .conf", ErrInvalidInput)
	}

	var (
		f   importedFields
		err error
	)
	switch {
	case strings.HasPrefix(trimmed, "{"):
		f, err = parseJSONAccount(trimmed)
	case looksLikeConf(trimmed):
		f, err = parseConfAccount(trimmed)
	case looksLikeTOML(trimmed):
		f, err = parseTOMLAccount(trimmed)
	default:
		return nil, fmt.Errorf("%w: поддерживаются warpscout-account.json, wgcf-account.toml и .conf", ErrInvalidInput)
	}
	if err != nil {
		return nil, err
	}
	return buildImportedAccount(f)
}

func looksLikeConf(s string) bool {
	low := strings.ToLower(s)
	if strings.Contains(low, "[interface]") || strings.Contains(low, "[peer]") {
		return true
	}
	for _, line := range strings.Split(s, "\n") {
		k, _, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "privatekey") {
			return true
		}
	}
	return false
}

func looksLikeTOML(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		k, _, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "private_key") {
			return true
		}
	}
	return false
}

func parseJSONAccount(raw string) (importedFields, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return importedFields{}, fmt.Errorf("%w: некорректный JSON: %v", ErrInvalidInput, err)
	}
	var out importedFields

	// Формат wdtt: {"data": {...}, "config": {...}} — разворачиваем сначала data, затем корень/config.
	if dataRaw, ok := top["data"]; ok && len(dataRaw) > 0 {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(dataRaw, &nested); err == nil {
			applyJSONAccountMap(nested, &out)
		}
	}
	applyJSONAccountMap(top, &out)
	return out, nil
}

func applyJSONAccountMap(m map[string]json.RawMessage, out *importedFields) {
	readStr := func(keys ...string) string {
		for _, k := range keys {
			if raw, ok := m[k]; ok {
				var v string
				if json.Unmarshal(raw, &v) == nil && strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			}
		}
		return ""
	}
	if v := readStr("id", "device_id", "deviceId"); v != "" {
		out.id = v
	}
	if v := readStr("token", "access_token", "accessToken"); v != "" {
		out.token = v
	}
	if v := readStr("private_key", "privateKey", "secret_key", "secretKey"); v != "" {
		out.privateKey = v
	}
	if v := readStr("peer_public_key", "peerPublicKey"); v != "" {
		out.peerPublicKey = v
	}
	if v := readStr("address_v4", "addressV4"); v != "" {
		out.addressV4 = v
	}
	if v := readStr("address_v6", "addressV6"); v != "" {
		out.addressV6 = v
	}
	if v := readStr("client_id", "clientId"); v != "" {
		out.clientID = v
	}
	if v := readStr("license", "license_key", "licenseKey"); v != "" {
		out.license = v
	}
	if v := readStr("account_type", "accountType"); v != "" {
		out.accountType = v
	}
	for _, k := range []string{"warp_plus", "warpPlus"} {
		if raw, ok := m[k]; ok {
			var b bool
			if json.Unmarshal(raw, &b) == nil {
				out.warpPlus = b
				out.hasWarpPlus = true
				break
			}
		}
	}
	if raw, ok := m["reserved"]; ok && len(raw) > 0 {
		var arr []int
		if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
			out.reserved = arr
		}
	}
	if accRaw, ok := m["account"]; ok && len(accRaw) > 0 {
		var meta accountMeta
		if json.Unmarshal(accRaw, &meta) == nil {
			if meta.AccountType != "" {
				out.accountType = meta.AccountType
			}
			if meta.License != "" {
				out.license = meta.License
			}
			if meta.Quota > 0 {
				out.quota = meta.Quota
			}
			if meta.PremiumData > 0 {
				out.premiumData = meta.PremiumData
			}
			if !out.hasWarpPlus {
				out.warpPlus = meta.WarpPlus
				out.hasWarpPlus = true
			}
		}
	}
	if cfgRaw, ok := m["config"]; ok && len(cfgRaw) > 0 {
		var cfg struct {
			ClientID string `json:"client_id"`
			Peers    []struct {
				PublicKey string `json:"public_key"`
			} `json:"peers"`
			Interface struct {
				Addresses struct {
					V4 string `json:"v4"`
					V6 string `json:"v6"`
				} `json:"addresses"`
			} `json:"interface"`
		}
		if json.Unmarshal(cfgRaw, &cfg) == nil {
			if cfg.ClientID != "" && out.clientID == "" {
				out.clientID = cfg.ClientID
			}
			if len(cfg.Peers) > 0 && cfg.Peers[0].PublicKey != "" && out.peerPublicKey == "" {
				out.peerPublicKey = cfg.Peers[0].PublicKey
			}
			if cfg.Interface.Addresses.V4 != "" && out.addressV4 == "" {
				out.addressV4 = cfg.Interface.Addresses.V4
			}
			if cfg.Interface.Addresses.V6 != "" && out.addressV6 == "" {
				out.addressV6 = cfg.Interface.Addresses.V6
			}
		}
	}
}

func parseConfAccount(raw string) (importedFields, error) {
	var out importedFields
	section := ""
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			commentBody := strings.TrimSpace(strings.TrimLeft(line, "#;"))
			if k, v, ok := strings.Cut(commentBody, "="); ok {
				switch strings.ToLower(strings.TrimSpace(k)) {
				case "reserved":
					if len(out.reserved) == 0 {
						out.reserved = parseReservedList(v)
					}
				case "clientid", "client_id":
					if out.clientID == "" {
						out.clientID = trimConfValue(v)
					}
				}
			}
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := trimConfValue(v)
		switch section {
		case "", "interface":
			switch key {
			case "privatekey":
				out.privateKey = val
			case "address":
				for _, part := range strings.Split(val, ",") {
					part = strings.TrimSpace(part)
					if part == "" {
						continue
					}
					if strings.Contains(part, ":") {
						if out.addressV6 == "" {
							out.addressV6 = part
						}
					} else if out.addressV4 == "" {
						out.addressV4 = part
					}
				}
			case "reserved":
				if nums := parseReservedList(val); len(nums) > 0 {
					out.reserved = nums
				}
			case "clientid", "client_id":
				out.clientID = val
			}
		case "peer":
			switch key {
			case "publickey":
				out.peerPublicKey = val
			case "reserved":
				if len(out.reserved) == 0 {
					if nums := parseReservedList(val); len(nums) > 0 {
						out.reserved = nums
					}
				}
			}
		}
	}
	return out, nil
}

func parseTOMLAccount(raw string) (importedFields, error) {
	var out importedFields
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := unquoteTOMLValue(v)
		switch key {
		case "private_key":
			out.privateKey = val
		case "device_id", "id":
			out.id = val
		case "access_token", "token":
			out.token = val
		case "license_key", "license":
			out.license = val
		case "peer_public_key":
			out.peerPublicKey = val
		case "address_v4":
			out.addressV4 = val
		case "address_v6":
			out.addressV6 = val
		case "client_id":
			out.clientID = val
		case "account_type":
			out.accountType = val
		}
	}
	return out, nil
}

func trimConfValue(v string) string {
	if idx := strings.IndexAny(v, "#;"); idx >= 0 {
		v = v[:idx]
	}
	return strings.TrimSpace(v)
}

func unquoteTOMLValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && ((v[0] == '\'' && v[len(v)-1] == '\'') || (v[0] == '"' && v[len(v)-1] == '"')) {
		return strings.TrimSpace(v[1 : len(v)-1])
	}
	if idx := strings.IndexByte(v, '#'); idx >= 0 {
		v = v[:idx]
	}
	return strings.TrimSpace(v)
}

func parseReservedList(raw string) []int {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return nil
	}
	var out []int
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// importedIDRe — id регистрации Cloudflare: UUID, у Zero Trust с префиксом "t.".
// Он уходит в путь запроса /reg/{id}, поэтому разделители и обход каталога
// отвергаются.
var importedIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// canonicalBase64 перекодирует значение заново: декодер пропускает \r и \n, а
// ключ ложится строкой в .conf, где перевод строки дописал бы чужие параметры.
func canonicalBase64(v string, size int) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
	if err != nil || len(raw) != size {
		return "", false
	}
	return base64.StdEncoding.EncodeToString(raw), true
}

func normalizeImportedIPv6(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if pfx, err := netip.ParsePrefix(v); err == nil && pfx.Addr().Is6() {
		return pfx.String(), nil
	}
	if addr, err := netip.ParseAddr(v); err == nil && addr.Is6() && addr.Zone() == "" {
		return addr.String(), nil
	}
	return "", fmt.Errorf("%w: некорректный IPv6-адрес", ErrInvalidInput)
}

func buildImportedAccount(f importedFields) (*Account, error) {
	if strings.TrimSpace(f.privateKey) == "" {
		return nil, fmt.Errorf("%w: отсутствует PrivateKey", ErrInvalidInput)
	}
	pub, err := PublicKeyOf(f.privateKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	priv, ok := canonicalBase64(f.privateKey, 32)
	if !ok {
		return nil, fmt.Errorf("%w: некорректный приватный ключ", ErrInvalidInput)
	}

	peerPub := strings.TrimSpace(f.peerPublicKey)
	if peerPub == "" {
		peerPub = WarpPeerPublicKey
	} else {
		if peerPub, ok = canonicalBase64(peerPub, 32); !ok {
			return nil, fmt.Errorf("%w: некорректный PublicKey пира WARP", ErrInvalidInput)
		}
	}

	addrV4 := DefaultTunnelIPv4
	if strings.TrimSpace(f.addressV4) != "" {
		norm, normErr := NormalizeTunnelIPv4(f.addressV4)
		if normErr != nil {
			return nil, normErr
		}
		addrV4 = norm
	}

	clientID := strings.TrimSpace(f.clientID)
	if clientID == "" && len(f.reserved) > 0 {
		if len(f.reserved) != 3 {
			return nil, fmt.Errorf("%w: reserved должен содержать 3 байта", ErrInvalidInput)
		}
		var rawRes [3]byte
		for i, n := range f.reserved {
			if n < 0 || n > 255 {
				return nil, fmt.Errorf("%w: байт reserved вне диапазона 0..255", ErrInvalidInput)
			}
			rawRes[i] = byte(n)
		}
		clientID = base64.StdEncoding.EncodeToString(rawRes[:])
	} else if clientID != "" {
		if clientID, ok = canonicalBase64(clientID, 3); !ok {
			return nil, fmt.Errorf("%w: некорректный client_id", ErrInvalidInput)
		}
	}

	addrV6, err := normalizeImportedIPv6(f.addressV6)
	if err != nil {
		return nil, err
	}

	rawID := strings.TrimSpace(f.id)
	if rawID != "" && !importedIDRe.MatchString(rawID) {
		return nil, fmt.Errorf("%w: некорректный id устройства", ErrInvalidInput)
	}
	token := strings.TrimSpace(f.token)
	hasToken := rawID != "" && token != ""
	if !hasToken {
		token = ""
	}
	id := rawID
	if id == "" {
		sum := sha256.Sum256([]byte(pub))
		id = "imported-" + hex.EncodeToString(sum[:4])
	}

	meta := accountMeta{
		AccountType: strings.TrimSpace(f.accountType),
		WarpPlus:    f.warpPlus,
		License:     strings.TrimSpace(f.license),
		Quota:       f.quota,
		PremiumData: f.premiumData,
	}
	warpPlus := meta.isWarpPlus()
	if f.hasWarpPlus && f.accountType == "" && f.quota == 0 && f.premiumData == 0 {
		warpPlus = f.warpPlus
	}
	acctType := meta.AccountType
	if acctType == "" {
		if hasToken {
			acctType = "free"
		} else {
			acctType = "imported"
		}
	}

	return &Account{
		ID:            id,
		Token:         token,
		PrivateKey:    priv,
		PublicKey:     pub,
		PeerPublicKey: peerPub,
		AddressV4:     addrV4,
		AddressV6:     addrV6,
		ClientID:      clientID,
		AccountType:   acctType,
		WarpPlus:      warpPlus,
		License:       strings.TrimSpace(f.license),
		HasToken:      hasToken,
		Imported:      true,
		CreatedAt:     time.Now().UTC(),
	}, nil
}
