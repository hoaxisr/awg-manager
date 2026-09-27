package obfuscator

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Пути сторожа (спека §4.9). ArmPath ставит cmd/awg-manager/paths.go.
// OopsPath — ТОЛЬКО /proc/mtdoops/oops: чтение соседних test (роняет роутер)
// и clear (стирает запись) здесь не допускается нигде.
var (
	ArmPath    = "/opt/etc/awg-manager/modules/awgm_relay.arming"
	BootIDPath = "/proc/sys/kernel/random/boot_id"
	OopsPath   = "/proc/mtdoops/oops"
)

// OopsReasonPrefix — начало причины срабатывания по oops (в отличие от метки
// boot_id): по нему applyObfWatchdog различает, повторится ли сигнал.
const OopsReasonPrefix = "oops в awgm_relay: "

var disarmMu sync.Mutex
var disarmTimer *time.Timer

// armStaleAfter — метка старше этого (по часам) не от сбоя: сбой = перезагрузка
// в первые 5 минут + загрузка + старт демона. Старше — демон не снял метку
// (упал до таймера), и любая будущая перезагрузка давала бы ложный trip (F477 M7).
// ponytail: часы после загрузки могут отставать (mtime в будущем) — тогда
// поведение прежнее, trip.
const armStaleAfter = 30 * time.Minute

func bootID() (string, error) {
	b, err := os.ReadFile(BootIDPath)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", errors.New("пустой boot_id")
	}
	return id, nil
}

// Arm — метка перед insmod: «модуль загружается в этой загрузке роутера».
func Arm() error {
	id, err := bootID()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(ArmPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(ArmPath, []byte(id), 0o644)
}

// DisarmAfter снимает метку через d (0 — сразу). Повторный вызов перевзводит.
func DisarmAfter(d time.Duration) {
	disarmMu.Lock()
	defer disarmMu.Unlock()
	if disarmTimer != nil {
		disarmTimer.Stop()
	}
	if d <= 0 {
		_ = os.Remove(ArmPath)
		return
	}
	disarmTimer = time.AfterFunc(d, func() { _ = os.Remove(ArmPath) })
}

// WatchdogCheck — на старте демона ДО первого Start. reason != "" — ядро
// выключить; hash — последний увиденный oops (сохранить, даже если не наш,
// чтобы старая запись не срабатывала повторно).
func WatchdogCheck(lastHash string) (reason, hash string) {
	if b, err := os.ReadFile(ArmPath); err == nil {
		cur, idErr := bootID()
		armed := strings.TrimSpace(string(b))
		switch {
		case idErr != nil || armed == "":
			// Без boot_id перезагрузку не доказать; метку оставляем таймеру.
			DisarmAfter(5 * time.Minute)
		case armed == cur:
			DisarmAfter(5 * time.Minute) // рестарт демона внутри окна — перевзвести
		default:
			if st, err := os.Stat(ArmPath); err != nil || time.Since(st.ModTime()) <= armStaleAfter {
				reason = "роутер перезагрузился в первые 5 минут после загрузки awgm_relay"
			}
			_ = os.Remove(ArmPath)
		}
	}
	raw, err := os.ReadFile(OopsPath)
	if err != nil {
		return reason, ""
	}
	var rec struct {
		Hash    string `json:"hash"`
		Content string `json:"content"`
	}
	if json.Unmarshal([]byte(strings.TrimRight(string(raw), "\x00")), &rec) != nil || rec.Hash == "" {
		return reason, ""
	}
	if rec.Hash != lastHash && OopsOurs(rec.Content) && reason == "" {
		reason = OopsReasonPrefix + firstOurFrame(rec.Content)
	}
	return reason, rec.Hash
}

// OopsOurs — наш ли трейс: кадр [awgm_relay] в строке PC/LR/epc/ra/трейса.
// «Modules linked in» перечисляет все загруженные модули — не признак.
func OopsOurs(content string) bool { return firstOurFrame(content) != "" }

func firstOurFrame(content string) string {
	inTrace := false
	for _, line := range strings.Split(content, "\n") {
		l := strings.ToLower(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(l, "call trace"):
			inTrace = true
			continue
		case strings.HasPrefix(l, "modules linked in"):
			continue
		}
		frame := inTrace || strings.HasPrefix(l, "pc is at") || strings.HasPrefix(l, "lr is at") ||
			strings.HasPrefix(l, "epc") || strings.HasPrefix(l, "ra ")
		if frame && strings.Contains(l, "[awgm_relay]") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
