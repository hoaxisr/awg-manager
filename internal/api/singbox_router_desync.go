package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// Потолок probe у awgm-box — 15 с; свой срок с запасом на старт процесса.
const desyncProbeTimeout = 10 * time.Second

// Срок одной пробы в серии «Подобрать»: кандидатов семь, идут по очереди
// (параллельно семь процессов awgm-box по ~40 МБ роутеру не по памяти).
const desyncScanProbeTimeout = 6 * time.Second

// DNS пробы — DoH: UDP-запросы к публичным резолверам провайдеры в РФ
// подменяют заглушками (стенд 27.11: rutor.info → адреса провайдера,
// youtube.com → NXDOMAIN даже у 1.1.1.1), и проба проверяла бы заглушку.
const desyncProbeDNS = "https://1.1.1.1/dns-query"

// desyncScanCandidates — что перебирает «Подобрать»: без desync (понять, нужен
// ли он вообще) и пресеты TCP-режимов. Пресет целиком, без полей формы:
// fake-поля при не-fake режиме форк отвергает.
var desyncScanCandidates = []struct {
	Label  string
	Fields router.DesyncFields
}{
	{"none", router.DesyncFields{Preset: "custom", Mode: "none"}},
	{"split", router.DesyncFields{Preset: "split"}},
	{"tlsrec", router.DesyncFields{Preset: "tlsrec"}},
	{"disorder", router.DesyncFields{Preset: "disorder"}},
	{"fake", router.DesyncFields{Preset: "fake"}},
	{"fake_split", router.DesyncFields{Preset: "fake_split"}},
	{"oob", router.DesyncFields{Preset: "oob"}},
}

// errProbeInput — awgm-box отверг аргументы или стратегию (ненулевой выход);
// сетевой отказ он отдаёт как success:false с кодом 0.
type errProbeInput struct{ msg string }

func (e errProbeInput) Error() string { return e.msg }

func runDesyncProbe(ctx context.Context, o router.Outbound, target, protocol string, timeout time.Duration) (SingboxRouterDesyncProbeResult, error) {
	var res SingboxRouterDesyncProbeResult
	opts, err := desyncProbeOptions(o)
	if err != nil {
		return res, errProbeInput{err.Error()}
	}
	f, err := os.CreateTemp("", "desync-probe-*.json")
	if err != nil {
		return res, err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(opts)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return res, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, singbox.DefaultBinaryPath(), "desync", "probe",
		"--options", f.Name(), "--target", target, "--protocol", protocol,
		"--timeout", timeout.String(), "--dns-server", desyncProbeDNS)
	cmd.WaitDelay = time.Second
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return res, errProbeInput{probeErrorText(stderr.String(), err)}
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return res, fmt.Errorf("decode probe output: %w", err)
	}
	return res, nil
}

type desyncProbeBody struct {
	Outbound router.Outbound `json:"outbound"`
	Target   string          `json:"target"`
	Protocol string          `json:"protocol"`
}

// decodeDesyncProbe читает и проверяет тело пробы; false — ответ уже записан.
func decodeDesyncProbe(w http.ResponseWriter, r *http.Request) (desyncProbeBody, bool) {
	var body desyncProbeBody
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return body, false
	}
	if err := decodeBody(r, &body); err != nil {
		response.BadRequest(w, err.Error())
		return body, false
	}
	body.Target = strings.TrimSpace(body.Target)
	// «-» в начале awgm-box принял бы за флаг.
	if body.Target == "" || strings.HasPrefix(body.Target, "-") {
		response.BadRequest(w, "target is required")
		return body, false
	}
	if body.Protocol != "tls" && body.Protocol != "http" {
		response.BadRequest(w, "protocol must be tls or http")
		return body, false
	}
	return body, true
}

// DesyncProbe runs one bounded `awgm-box desync probe` against the strategy
// from the form (saved or not) — the running config is not touched.
//
//	@Summary		Probe a desync strategy
//	@Description	Runs `awgm-box desync probe` once with the given desync outbound fields (TLS handshake or HTTP GET to target, DNS over HTTPS 1.1.1.1). Network failure is success=false with error; invalid strategy is 400.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterDesyncProbeRequest	true	"Strategy + target"
//	@Success		200		{object}	SingboxRouterDesyncProbeResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Router			/singbox/router/desync/probe [post]
func (h *SingboxRouterHandler) DesyncProbe(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeDesyncProbe(w, r)
	if !ok {
		return
	}
	res, err := runDesyncProbe(r.Context(), body.Outbound, body.Target, body.Protocol, desyncProbeTimeout)
	var input errProbeInput
	switch {
	case errors.As(err, &input):
		response.BadRequest(w, input.msg)
	case err != nil:
		response.InternalError(w, err.Error())
	default:
		response.Success(w, res)
	}
}

// DesyncScan probes the target once per candidate mode, sequentially.
//
//	@Summary		Scan desync modes against a target
//	@Description	Runs `awgm-box desync probe` for each candidate (no desync, split, tlsrec, disorder, fake, fake_split, oob) one after another. Only target and protocol (and bind_interface of outbound) are used. A candidate the platform rejects comes back with success=false and the reason.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterDesyncProbeRequest	true	"Target (outbound fields other than bind_interface are ignored)"
//	@Success		200		{object}	SingboxRouterDesyncScanResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Router			/singbox/router/desync/scan [post]
func (h *SingboxRouterHandler) DesyncScan(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeDesyncProbe(w, r)
	if !ok {
		return
	}
	out := make([]SingboxRouterDesyncScanRow, 0, len(desyncScanCandidates))
	for _, c := range desyncScanCandidates {
		o := router.Outbound{Type: "desync", Tag: "scan", BindInterface: body.Outbound.BindInterface, DesyncFields: c.Fields}
		res, err := runDesyncProbe(r.Context(), o, body.Target, body.Protocol, desyncScanProbeTimeout)
		if r.Context().Err() != nil {
			return // вкладку закрыли — остальные пробы не нужны
		}
		if err != nil {
			res = SingboxRouterDesyncProbeResult{Target: body.Target, Protocol: body.Protocol, Error: err.Error()}
		}
		out = append(out, SingboxRouterDesyncScanRow{Label: c.Label, Preset: c.Fields.Preset, Mode: c.Fields.Mode, Result: res})
	}
	response.Success(w, out)
}

// checkDesyncOutbound прогоняет поля desync-outbound через `awgm-box desync
// check` — валидатор самого форка. Без этого неверное значение (опечатка в
// режиме, ttl вне 1..255) сохранится в выключенный слот, а sing-box check
// упадёт только при его включении. Прочие типы — nil.
func checkDesyncOutbound(ctx context.Context, o router.Outbound) error {
	if o.Type != "desync" {
		return nil
	}
	opts, err := desyncProbeOptions(o)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, singbox.DefaultBinaryPath(), "desync", "check")
	cmd.Stdin = strings.NewReader(string(opts))
	cmd.WaitDelay = time.Second
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("desync: %s", probeErrorText(stderr.String(), err))
	}
	return nil
}

// probeErrorText — строка «Error: …» из stderr cobra без следующей за ней
// справки по флагам; иначе stderr целиком.
func probeErrorText(stderr string, err error) string {
	for _, line := range strings.Split(stderr, "\n") {
		if msg, ok := strings.CutPrefix(line, "Error: "); ok {
			return msg
		}
	}
	return strings.TrimSpace(stderr + " " + err.Error())
}

// desyncProbeOptions — JSON стратегии для --options: поля outbound без type/tag
// (DesyncOptions форка отвергает незнакомые ключи) и без domain_resolver —
// в отдельном Box пробы тегов DNS-серверов роутера нет.
func desyncProbeOptions(o router.Outbound) ([]byte, error) {
	if o.Type != "desync" {
		return nil, fmt.Errorf("outbound type must be desync")
	}
	o.DomainResolver = nil
	raw, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	delete(m, "type")
	delete(m, "tag")
	return json.Marshal(m)
}
