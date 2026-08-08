// Package awgmbackend инкапсулирует всё, что нужно для применения правил
// через собственную таблицу xtables awgm: проверку доступности бандла,
// загрузку модулей и запуск статического iptables из бандла.
//
// Зачем это существует: правила, созданные в таблице awgm, живут отдельно от
// legacy-таблиц filter/nat/mangle, а ndm при перестройке firewall переписывает
// только их — то есть наши правила переживают перестройку вместо того, чтобы
// исчезать вместе с ней.
package awgmbackend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// BundleDir — куда IPK разворачивает бандл. НЕ /opt/lib: соглашение то же,
// что у прочих наших бандлов — ничего в общие каталоги Entware.
const BundleDir = "/opt/lib/awg-manager/awgm"

// requiredModules — порядок и есть порядок загрузки: сначала таблица, потом
// таргеты. У всех трёх depends= пуст (все символы из vmlinux), многопроходная
// загрузка не нужна.
var requiredModules = []string{"iptable_awgm.ko", "xt_AWGMTPROXY.ko", "xt_AWGMPPE.ko"}

type Backend struct {
	dir string
	// model — РЕЗОЛВЕР модели роутера, а не её значение. Модель приходит из
	// кешированной информации NDMS, и на старте демона она бывает ещё пустой:
	// захваченная один раз, пустая строка держала бы бэкенд недоступным до
	// перезапуска демона. Ленивый вызов лечит это сам, как только NDMS ответит.
	model  func() string
	loaded func(name string) bool
	insmod func(ctx context.Context, path string) error
	dmesg  func(ctx context.Context) (string, error)
}

func New(dir string, model func() string) *Backend {
	b := &Backend{dir: dir, model: model}
	b.loaded = moduleLoaded
	b.insmod = func(ctx context.Context, path string) error {
		result, err := sysexec.Run(ctx, "insmod", path)
		return sysexec.FormatError(result, err)
	}
	b.dmesg = dmesgTail
	return b
}

// kernelReasonMaxLines — сколько строк ядра максимум приклеиваем к ошибке.
// Причина обычно занимает одну строку; трёх хватает на «Unknown symbol» с
// продолжением, и текст ошибки не превращается в дамп буфера.
const kernelReasonMaxLines = 3

// dmesgTail читает хвост кольцевого буфера ядра. Именно `dmesg`, а не
// `dmesg -c`: буфер только читаем, чистить его нельзя — он общий, и его
// содержимое ещё понадобится и нам, и диагностике роутера. Шелл-аут вместо
// разбора /dev/kmsg — тот же приём, что уже используется в проекте для
// OOM-классификации sing-box и в диагностике awg-proxy.
func dmesgTail(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := sysexec.Shell(ctx, "dmesg | tail -n 200")
	if err != nil {
		return "", err
	}
	return result.Stdout, nil
}

// kernelReasonLines выбирает из вывода ядра последние строки, упоминающие
// модуль. Настоящую причину отказа знает только ядро: insmod видит лишь
// ENOEXEC и печатает бесполезное «invalid module format», а «exports duplicate
// symbol …», «Unknown symbol …» и несовпадение vermagic уходят в dmesg.
func kernelReasonLines(module, out string) []string {
	var hits []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && strings.Contains(line, module) {
			hits = append(hits, line)
		}
	}
	if len(hits) > kernelReasonMaxLines {
		hits = hits[len(hits)-kernelReasonMaxLines:]
	}
	return hits
}

// kernelReason — best-effort: буфер недоступен или про модуль там ничего нет,
// значит добавки к ошибке просто не будет.
func (b *Backend) kernelReason(ctx context.Context, module string) string {
	if b.dmesg == nil {
		return ""
	}
	out, err := b.dmesg(ctx)
	if err != nil {
		return ""
	}
	return strings.Join(kernelReasonLines(module, out), "; ")
}

// moduleLoaded читает /proc/modules — modprobe в busybox нет, а lsmod
// порождает лишний процесс на каждый из трёх модулей.
func moduleLoaded(name string) bool {
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, name+" ") {
			return true
		}
	}
	return false
}

// Load поднимает модули таблицы awgm. insmod не переживает перезагрузку
// роутера, поэтому вызывается на каждом включении режима, а не один раз.
// Частичная загрузка = отказ целиком.
func (b *Backend) Load(ctx context.Context) error {
	for _, name := range requiredModules {
		base := strings.TrimSuffix(name, ".ko")
		if b.loaded(base) {
			continue
		}
		path := filepath.Join(b.dir, "modules", name)
		err := b.insmod(ctx, path)
		if err == nil && b.loaded(base) {
			continue
		}
		if err == nil {
			err = errors.New("insmod прошёл, но модуль не появился в /proc/modules")
		}
		// Только на пути отказа: без причины из ядра в журнале остаётся
		// «invalid module format», по которому диагностировать нечего.
		if reason := b.kernelReason(ctx, base); reason != "" {
			return fmt.Errorf("модуль %s не загрузился: %w (ядро: %s)", base, err, reason)
		}
		return fmt.Errorf("модуль %s не загрузился: %w", base, err)
	}
	return nil
}

// binary возвращает путь до аплета бандла. Бинарь статический, аплеты
// выбираются по argv[0] через симлинки на iptables-awgm.
func (b *Backend) binary(applet string) string {
	return filepath.Join(b.dir, "sbin", applet)
}

// waitArgs добавляет `-w`, как это делает обёртка legacy-бинаря: параллельная
// перестройка firewall роутера может держать xtables-лок, и без ожидания
// команда просто отказала бы.
func waitArgs(args []string) []string {
	return append([]string{"-w"}, args...)
}

func (b *Backend) Run(ctx context.Context, args ...string) error {
	result, err := sysexec.Run(ctx, b.binary("iptables"), waitArgs(args)...)
	return sysexec.FormatError(result, err)
}

func (b *Backend) RunOutput(ctx context.Context, args ...string) (string, error) {
	result, err := sysexec.Run(ctx, b.binary("iptables"), waitArgs(args)...)
	if err != nil {
		return "", sysexec.FormatError(result, err)
	}
	if result == nil {
		return "", nil
	}
	return result.Stdout, nil
}

// RestoreNoflush применяет блоб через iptables-restore --noflush.
// Форма ретраев та же, что у sysiptables.RestoreNoflush — три попытки с
// линейным бэкоффом, потому что параллельная перестройка ndm может держать
// xtables-лок; шаг бэкоффа свой, 200 мс против секунды там.
func (b *Backend) RestoreNoflush(ctx context.Context, input string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
		}
		result, err := sysexec.RunWithOptions(ctx,
			b.binary("iptables-restore"), []string{"--noflush"},
			sysexec.Options{Stdin: strings.NewReader(input)})
		if err == nil {
			return nil
		}
		lastErr = sysexec.FormatError(result, err)
	}
	return fmt.Errorf("iptables-restore --noflush (awgm): %w", lastErr)
}

// Available сообщает, можно ли включать режим awgm. Без побочных эффектов:
// модули не грузит, файлы не создаёт. Вторым значением — причина отказа,
// пригодная для показа пользователю.
//
// Гейт по модели обязателен: IPK общий на архитектуру, а бандл собран под
// конкретную модель. Без гейта на чужой aarch64-модели файлы найдутся, а
// insmod упадёт по vermagic — и пользователь получит «не работает» вместо
// «недоступно на вашей модели».
func (b *Backend) Available() (bool, string) {
	raw, err := os.ReadFile(filepath.Join(b.dir, "MODEL"))
	if err != nil {
		return false, "бандл awgm не установлен"
	}
	model := ""
	if b.model != nil {
		model = b.model()
	}
	if builtFor := strings.TrimSpace(string(raw)); builtFor != model {
		if model == "" {
			return false, fmt.Sprintf("бандл собран под %s, модель роутера ещё не известна", builtFor)
		}
		return false, fmt.Sprintf("бандл собран под %s, роутер — %s", builtFor, model)
	}
	if _, err := os.Stat(filepath.Join(b.dir, "sbin", "iptables")); err != nil {
		return false, "в бандле нет iptables-awgm"
	}
	for _, m := range requiredModules {
		if _, err := os.Stat(filepath.Join(b.dir, "modules", m)); err != nil {
			return false, "в бандле нет модуля " + m
		}
	}
	return true, ""
}
