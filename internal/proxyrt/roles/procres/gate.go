package procres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
)

// probeTimeout — §4 протокола: проба живёт 3 секунды, дальше убивается.
const probeTimeout = 3 * time.Second

// Gate — гейт пригодности бинаря: проба --awgm-protocol БЕЗ запуска
// инстанса. Кэш по идентичности файла (путь+mtime+размер): проба стоит
// запуска процесса, а бинарь меняется только установкой.
type Gate struct {
	mu    sync.Mutex
	cache map[gateKey]error

	// runProbe — шов для livetest не нужен: тесты кормят настоящие скрипты.
	// Поле отсутствует намеренно (G4): единственный исполнитель — exec.
}

type gateKey struct {
	path  string
	mtime int64
	size  int64
}

func NewGate() *Gate {
	return &Gate{cache: make(map[gateKey]error)}
}

func (g *Gate) Check(ctx context.Context, binary, impl, role string, needCmds []string) error {
	st, err := os.Stat(binary)
	if err != nil {
		return fmt.Errorf("бинарь %s: %w", binary, err)
	}
	key := gateKey{path: binary, mtime: st.ModTime().UnixNano(), size: st.Size()}
	g.mu.Lock()
	cached, ok := g.cache[key]
	g.mu.Unlock()
	if ok {
		return cached
	}
	verdict, cacheable := g.probe(ctx, binary, impl, role, needCmds)
	if cacheable {
		// Кэшируются только РАЗОБРАННЫЕ вердикты (версия/impl/команды/код
		// выхода бинаря): они детерминированы содержимым файла. Ошибка самого
		// запуска пробы (ENOMEM, fork fail) — временная, её кэшировать
		// нельзя: залипла бы до переустановки бинаря (M8 ревью).
		g.mu.Lock()
		g.cache[key] = verdict
		g.mu.Unlock()
	}
	return verdict
}

func (g *Gate) probe(ctx context.Context, binary, impl, role string, needCmds []string) (verdict error, cacheable bool) {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(pctx, binary, "--awgm-protocol")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		// Ненулевой выход = бинарь запустился и флага не понял — свойство
		// файла, кэшируемо. Всё прочее — процесс не запустился, не кэшируем.
		cacheable = errors.As(err, &exit)
		return fmt.Errorf("проба --awgm-protocol %s: %v — пин бинаря не обновлён", binary, err), cacheable
	}
	// Ровно одна JSON-строка и ничего больше (§4).
	line := bytes.TrimSpace(out.Bytes())
	var info awgmproto.ProtocolInfo
	if err := json.Unmarshal(line, &info); err != nil {
		return fmt.Errorf("проба %s: вывод не разобран (%v) — пин бинаря не обновлён", binary, err), true
	}
	if info.V != awgmproto.Version {
		return fmt.Errorf("бинарь %s говорит на версии протокола %d, менеджер на %d — пин бинаря не обновлён",
			binary, info.V, awgmproto.Version), true
	}
	if info.Impl != impl {
		return fmt.Errorf("бинарь %s: impl %q, ожидали %q", binary, info.Impl, impl), true
	}
	if info.Role != role {
		return fmt.Errorf("бинарь %s: role %q, ожидали %q", binary, info.Role, role), true
	}
	have := make(map[string]bool, len(info.Commands))
	for _, c := range info.Commands {
		have[c] = true
	}
	for _, need := range needCmds {
		if !have[need] {
			return fmt.Errorf("бинарь %s не поддерживает команду %q", binary, need), true
		}
	}
	return nil, true
}
