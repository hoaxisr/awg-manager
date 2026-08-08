package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// prermAwgmBlock вырезает из entware/control/prerm блок уборки правил awgm —
// от строки с AWGM=... до закрывающего `fi` в нулевой колонке — и подставляет
// свой каталог бандла. Прогонять prerm целиком нельзя: он останавливает демон
// и зовёт /opt/bin/awg-manager по абсолютным путям.
func prermAwgmBlock(t *testing.T, bundleDir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "entware", "control", "prerm"))
	if err != nil {
		t.Fatalf("прочитать prerm: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "AWGM=") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("в prerm не нашлось строки AWGM=... — блок уборки awgm переименован или удалён")
	}
	for i := start; i < len(lines); i++ {
		if lines[i] == "fi" {
			block := append([]string{"AWGM=" + bundleDir}, lines[start+1:i+1]...)
			return strings.Join(block, "\n") + "\n"
		}
	}
	t.Fatal("блок уборки awgm в prerm не закрыт `fi`")
	return ""
}

// fakeAwgmBinary кладёт заглушку iptables, которая просто логирует "$*" и
// выходит успешно. Состояние таблицы тут не нужно: в отличие от nft-версии
// (где чужие правила надо было не задеть), таблица awgm наша эксклюзивно —
// проверяем только СОСТАВ поданных команд, не их эффект.
func fakeAwgmBinary(t *testing.T) (bundleDir, logPath string) {
	t.Helper()
	bundleDir = t.TempDir()
	logPath = filepath.Join(bundleDir, "log")
	binPath := filepath.Join(bundleDir, "sbin", "iptables")
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
echo "$*" >> ` + shQuote(logPath) + `
exit 0
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bundleDir, logPath
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Удаление пакета обязано чистить ТОЛЬКО таблицу awgm: флаш всей таблицы,
// удаление трёх цепочек, выгрузка модулей. Штатные mangle/nat не трогаются
// ни одной командой — они не наши.
func TestPrermFlushesAwgmTableOnly(t *testing.T) {
	bundleDir, logPath := fakeAwgmBinary(t)
	cmd := exec.Command("sh", "-c", prermAwgmBlock(t, bundleDir))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("блок prerm упал: %v\n%s", err, out)
	}
	log, _ := os.ReadFile(logPath)
	for _, want := range []string{
		"-t awgm -F",
		"-t awgm -X AWGM-TPROXY",
		"-t awgm -X AWGM-REDIRECT",
		"-t awgm -X AWGM-BLACKHOLE",
	} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("нет команды %q:\n%s", want, log)
		}
	}
	for _, forbidden := range []string{"-t mangle", "-t nat"} {
		if strings.Contains(string(log), forbidden) {
			t.Fatalf("prerm полез в чужую таблицу (%s):\n%s", forbidden, log)
		}
	}
}

// Бандла на диске нет (например, снесён раньше) — блок обязан промолчать и
// не упасть: `-x "$AWGM/sbin/iptables"` не проходит, и весь `if` пропускается.
func TestPrermSkipsWhenBundleMissing(t *testing.T) {
	cmd := exec.Command("sh", "-c", prermAwgmBlock(t, filepath.Join(t.TempDir(), "no-such-dir")))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("блок prerm упал при отсутствующем бандле: %v\n%s", err, out)
	}
}
