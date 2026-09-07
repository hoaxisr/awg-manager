package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
)

// ── DTO ──────────────────────────────────────────────────────────

// AwgAnalyzeRequest — тело POST /awg/analyze. conf обязателен; tunnelId
// подставляет PrivateKey/PresharedKey из хранилища там, где их нет в тексте
// (GET туннеля ключей не отдаёт — F56/F70).
type AwgAnalyzeRequest struct {
	Conf     string `json:"conf" example:"[Interface]\nPrivateKey = ...\n"`
	TunnelID string `json:"tunnelId,omitempty" example:"awg1"`
}

// AwgAnalyzeIssue — ошибка совместимости или предупреждение. Поле с именем
// параметра не возвращается: сообщение его уже называет.
type AwgAnalyzeIssue struct {
	Code    string `json:"code" example:"hp_padding_min"`
	Message string `json:"message" example:"Описание ошибки или предупреждения"`
}

// AwgAnalyzeInterface — секция [Interface] без ключевого материала.
type AwgAnalyzeInterface struct {
	Jc   int    `json:"jc" example:"4"`
	Jmin int    `json:"jmin" example:"50"`
	Jmax int    `json:"jmax" example:"1000"`
	S1   int    `json:"s1" example:"12"`
	S2   int    `json:"s2" example:"5"`
	S3   int    `json:"s3" example:"12"`
	S4   int    `json:"s4" example:"12"`
	H1   string `json:"h1" example:"1"`
	H2   string `json:"h2" example:"2"`
	H3   string `json:"h3" example:"3"`
	H4   string `json:"h4" example:"4"`
	I1   string `json:"i1" example:"<b 0xc0000001>"`
	I2   string `json:"i2" example:""`
	I3   string `json:"i3" example:""`
	I4   string `json:"i4" example:""`
	I5   string `json:"i5" example:""`
	// HeaderProtection — ключ задан и валиден (32 байта base64). Сам ключ
	// не возвращается.
	HeaderProtection       bool   `json:"headerProtection" example:"true"`
	ContentPaddingAddition string `json:"contentPaddingAddition" example:"0-64"`
	RekeyAfterTime         string `json:"rekeyAfterTime" example:""`
	RekeyTimeout           string `json:"rekeyTimeout" example:""`
	RejectAfterTime        string `json:"rejectAfterTime" example:""`
	KeepaliveTimeout       string `json:"keepaliveTimeout" example:""`
	MaxHandshakeAttempts   string `json:"maxHandshakeAttempts" example:""`
	RandomTrailers         bool   `json:"randomTrailers" example:"true"`
	DisableCookies         bool   `json:"disableCookies" example:"false"`
	MTU                    int    `json:"mtu" example:"1420"`
	// MTUSet — ключ MTU есть в тексте; иначе значение выше — дефолт парсера
	// (1280), а не выбор пользователя.
	MTUSet  bool   `json:"mtuSet" example:"true"`
	Address string `json:"address" example:"10.8.0.2/32"`
	DNS     string `json:"dns" example:"10.8.0.1"`
}

// AwgAnalyzePeer — секция [Peer] без ключевого материала.
type AwgAnalyzePeer struct {
	Endpoint            string   `json:"endpoint" example:"vpn.example.com:51820"`
	AllowedIPs          []string `json:"allowedIPs" example:"0.0.0.0/0,::/0"`
	AllowedIPsSet       bool     `json:"allowedIPsSet" example:"true"`
	PersistentKeepalive string   `json:"persistentKeepalive" example:"25-35"`
	KeepaliveSet        bool     `json:"keepaliveSet" example:"true"`
	HasPresharedKey     bool     `json:"hasPresharedKey" example:"true"`
	// PresharedKeyFromStore — PSK взят из хранилища по tunnelId, в тексте
	// его не было.
	PresharedKeyFromStore bool `json:"presharedKeyFromStore" example:"false"`
}

// AwgAnalyzeData — результат анализа.
type AwgAnalyzeData struct {
	Version   string              `json:"version" example:"awg3.1"`
	Interface AwgAnalyzeInterface `json:"interface"`
	Peer      AwgAnalyzePeer      `json:"peer"`
	Errors    []AwgAnalyzeIssue   `json:"errors"`
	Warnings  []AwgAnalyzeIssue   `json:"warnings"`
}

// AwgAnalyzeResponse — конверт POST /awg/analyze.
type AwgAnalyzeResponse struct {
	Success bool           `json:"success" example:"true"`
	Data    AwgAnalyzeData `json:"data"`
}

// ── Ядро ─────────────────────────────────────────────────────────

// confHasKey — в секции section ("[interface]"/"[peer]") текста есть строка
// key с непустым значением. Нужен, чтобы отличить «задано в тексте» от
// дефолта, который подставляет config.Parse.
func confHasKey(conf, section, key string) bool {
	cur := ""
	for _, line := range strings.Split(conf, "\n") {
		l := strings.TrimSpace(line)
		switch strings.ToLower(l) {
		case "[interface]", "[peer]":
			cur = strings.ToLower(l)
			continue
		}
		if cur != section {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), key) && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// mergeStoredKeys вставляет PrivateKey после [Interface] и PresharedKey
// после [Peer] из хранилища, если в тексте таких строк нет. Явная строка в
// тексте всегда побеждает — та же merge-семантика, что у update.
//
// Пустая строка ключа (`PrivateKey =`) в тексте не считается «есть в
// тексте» (confHasKey требует непустое значение), поэтому её сначала нужно
// убрать: иначе она добавляется в файл ПОСЛЕ вставленной из хранилища
// строки и config.Parse берёт значение из последней встреченной строки —
// пустое побеждает молча. config.Generate пишет `PrivateKey = %s`
// безусловно, а tunnels_view.go затирает ключ пустой строкой — этот случай
// реально приходит с фронта.
func mergeStoredKeys(conf string, stored *storage.AWGTunnel) string {
	if stored == nil {
		return conf
	}
	has := func(section, key string) bool { return confHasKey(conf, section, key) }
	removeEmptyKeyLine := func(section, key string) {
		cur := ""
		out := make([]string, 0, strings.Count(conf, "\n")+1)
		for _, l := range strings.Split(conf, "\n") {
			trimmed := strings.TrimSpace(l)
			switch strings.ToLower(trimmed) {
			case "[interface]", "[peer]":
				cur = strings.ToLower(trimmed)
				out = append(out, l)
				continue
			}
			if cur == section {
				if k, v, ok := strings.Cut(trimmed, "="); ok && strings.EqualFold(strings.TrimSpace(k), key) && strings.TrimSpace(v) == "" {
					continue
				}
			}
			out = append(out, l)
		}
		conf = strings.Join(out, "\n")
	}
	insertAfter := func(section, line string) {
		out := make([]string, 0, strings.Count(conf, "\n")+2)
		for _, l := range strings.Split(conf, "\n") {
			out = append(out, l)
			if strings.EqualFold(strings.TrimSpace(l), section) {
				out = append(out, line)
			}
		}
		conf = strings.Join(out, "\n")
	}
	if stored.Interface.PrivateKey != "" && !has("[interface]", "PrivateKey") {
		removeEmptyKeyLine("[interface]", "PrivateKey")
		insertAfter("[Interface]", "PrivateKey = "+stored.Interface.PrivateKey)
	}
	if stored.Peer.PresharedKey != "" && !has("[peer]", "PresharedKey") {
		removeEmptyKeyLine("[peer]", "PresharedKey")
		insertAfter("[Peer]", "PresharedKey = "+stored.Peer.PresharedKey)
	}
	return conf
}

// moduleSupports — версия модуля ядра ("3.1.20260906") не ниже major.minor.
// Пустая или нечитаемая версия считается поддерживающей: предупреждать
// нечем, а ложное «модуль старый» хуже молчания.
func moduleSupports(version string, major, minor int) bool {
	parts := strings.SplitN(strings.TrimSpace(version), ".", 3)
	if len(parts) < 2 {
		return true
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return true
	}
	return ma > major || (ma == major && mi >= minor)
}

func validHPKey(key string) bool {
	b, err := base64.StdEncoding.DecodeString(key)
	return err == nil && len(b) == 32
}

// analyzeAwgConf — чистое ядро: парсинг, классификация, совместимость.
// kmodVersion — версия модуля ядра (loaded или on-disk), "" если неизвестна.
func analyzeAwgConf(conf string, stored *storage.AWGTunnel, kmodVersion string) (AwgAnalyzeData, error) {
	pskInText := confHasKey(conf, "[peer]", "PresharedKey")
	t, err := config.Parse(mergeStoredKeys(conf, stored))
	if err != nil {
		return AwgAnalyzeData{}, err
	}
	iface := t.Interface
	o := &iface.AWGObfuscation
	d := AwgAnalyzeData{
		Version: config.ClassifyAWGVersion(&iface),
		Interface: AwgAnalyzeInterface{
			Jc: o.Jc, Jmin: o.Jmin, Jmax: o.Jmax,
			S1: o.S1, S2: o.S2, S3: o.S3, S4: o.S4,
			H1: o.H1, H2: o.H2, H3: o.H3, H4: o.H4,
			I1: o.I1, I2: o.I2, I3: o.I3, I4: o.I4, I5: o.I5,
			HeaderProtection:       o.HeaderProtectionKey != "" && validHPKey(o.HeaderProtectionKey),
			ContentPaddingAddition: o.ContentPaddingAddition,
			RekeyAfterTime:         o.RekeyAfterTime,
			RekeyTimeout:           o.RekeyTimeout,
			RejectAfterTime:        o.RejectAfterTime,
			KeepaliveTimeout:       o.KeepaliveTimeout,
			MaxHandshakeAttempts:   o.MaxHandshakeAttempts,
			RandomTrailers:         iface.RandomTrailers,
			DisableCookies:         iface.DisableCookies,
			MTU:                    iface.MTU,
			MTUSet:                 confHasKey(conf, "[interface]", "MTU"),
			Address:                iface.Address,
			DNS:                    iface.DNS,
		},
		Peer: AwgAnalyzePeer{
			Endpoint:              t.Peer.Endpoint,
			AllowedIPs:            append([]string{}, t.Peer.AllowedIPs...),
			AllowedIPsSet:         confHasKey(conf, "[peer]", "AllowedIPs"),
			PersistentKeepalive:   t.Peer.PersistentKeepalive.String(),
			KeepaliveSet:          confHasKey(conf, "[peer]", "PersistentKeepalive"),
			HasPresharedKey:       t.Peer.PresharedKey != "",
			PresharedKeyFromStore: t.Peer.PresharedKey != "" && !pskInText,
		},
		Errors:   []AwgAnalyzeIssue{},
		Warnings: []AwgAnalyzeIssue{},
	}

	if err := config.ValidateAWG3(o); err != nil {
		code := "hp_padding_min"
		if !validHPKey(o.HeaderProtectionKey) {
			code = "hp_key_invalid"
		}
		d.Errors = append(d.Errors, AwgAnalyzeIssue{Code: code, Message: err.Error()})
	}
	if err := config.ValidateHeaderRanges(o); err != nil {
		code := "h_overlap"
		if errors.Is(err, config.ErrHeaderFormat) {
			code = "h_invalid"
		}
		d.Errors = append(d.Errors, AwgAnalyzeIssue{Code: code, Message: err.Error()})
	}

	// Гейт модуля ядра — только для kernel-бэкенда. NativeWG несёт awg_proxy
	// ≥ 1.4.0 в самой сборке, ему предупреждать не о чем.
	kernelBackend := stored == nil || stored.Backend == "" || stored.Backend == "kernel"
	if kernelBackend {
		switch d.Version {
		case "awg3":
			if !moduleSupports(kmodVersion, 3, 0) {
				d.Warnings = append(d.Warnings, AwgAnalyzeIssue{Code: "module_below_awg3",
					Message: fmt.Sprintf("Параметры AWG 3.0 требуют модуль ядра 3.x, на роутере %s — они будут проигнорированы", kmodVersion)})
			}
		case "awg3.1":
			if !moduleSupports(kmodVersion, 3, 1) {
				d.Warnings = append(d.Warnings, AwgAnalyzeIssue{Code: "module_below_awg31",
					Message: fmt.Sprintf("Флаги AWG 3.1 требуют модуль ядра 3.1+, на роутере %s — они будут проигнорированы", kmodVersion)})
			}
		}
	}
	return d, nil
}

// ── Handler ──────────────────────────────────────────────────────

// AwgAnalyzeHandler — POST /api/awg/analyze.
type AwgAnalyzeHandler struct {
	store  *storage.AWGTunnelStore
	loader *kmod.Loader // nil в тестах и там, где модуля нет
}

func NewAwgAnalyzeHandler(store *storage.AWGTunnelStore, loader *kmod.Loader) *AwgAnalyzeHandler {
	return &AwgAnalyzeHandler{store: store, loader: loader}
}

func (h *AwgAnalyzeHandler) kmodVersion() string {
	if h.loader == nil {
		return ""
	}
	if v := h.loader.LoadedVersion(); v != "" {
		return v
	}
	return h.loader.OnDiskVersion()
}

// Analyze разбирает .conf и возвращает версию, поля без ключей и ошибки
// совместимости.
//
//	@Summary		Анализ AWG/WireGuard .conf
//	@Description	Классификация версии, нормализованные поля без ключевого материала, ошибки совместимости с модулем и предупреждения о версии модуля. tunnelId подставляет PrivateKey/PresharedKey из хранилища, если их нет в тексте.
//	@Tags			tunnels
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			request	body		AwgAnalyzeRequest	true	"Текст conf и опциональный tunnelId"
//	@Success		200		{object}	AwgAnalyzeResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/awg/analyze [post]
func (h *AwgAnalyzeHandler) Analyze(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[AwgAnalyzeRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	if strings.TrimSpace(req.Conf) == "" {
		response.Error(w, "Вставьте содержимое .conf файла AmneziaWG / WireGuard", "MISSING_CONF")
		return
	}
	var stored *storage.AWGTunnel
	if req.TunnelID != "" {
		t, err := h.store.Get(req.TunnelID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				response.ErrorWithStatus(w, http.StatusNotFound, "Туннель не найден", "NOT_FOUND")
				return
			}
			response.InternalError(w, err.Error())
			return
		}
		stored = t
	}
	data, err := analyzeAwgConf(req.Conf, stored, h.kmodVersion())
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_CONF")
		return
	}
	response.Success(w, data)
}
