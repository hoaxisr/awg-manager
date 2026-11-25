package firc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Сторонний firc — самостоятельный продукт со своим интерфейсом: каталоги
// /opt/etc/firc и /opt/var/lib/firc, сокет и правила netfilter у него те же, что
// и у нашего рантайма, а форматы файлов взаимно не читаются. Поэтому при нём
// панель не управляет FIRC вовсе и его состояние не показывает. Признак один на
// всех — ForeignDetector; остальной пакет спрашивает CheckNotForeign/ForeignFirc.

// ErrForeignFirc — отказ любой мутации FIRC при установленном стороннем firc.
var ErrForeignFirc = errors.New("на роутере установлен firc: он управляется своим интерфейсом, движок FIRC панели доступен только там, где firc не установлен")

// ForeignFirc описывает найденный сторонний firc.
type ForeignFirc struct {
	// Package — пакет Entware, которому принадлежат файлы firc; пусто, если
	// бинарь поставлен руками.
	Package string `json:"package,omitempty"`
	// Binary — чужой бинарь fircd; заполнен только у поставленного руками.
	Binary string `json:"binary,omitempty"`
	// WebPort/WebHost — веб-интерфейс firc, если он включён и порт задан в его
	// конфиге явно. WebHost пуст, когда firc слушает все адреса.
	WebPort int    `json:"webPort,omitempty"`
	WebHost string `json:"webHost,omitempty"`
}

const (
	opkgInfoDir          = "/opt/lib/opkg/info"
	runtimeOperationFile = "/opt/etc/awg-manager/firc-runtime-operation.json"
	fircdBinLink         = "/opt/bin/fircd"
)

// ownRuntimePackages — пакеты, чьи файлы firc наши. Пакет awg-manager здесь
// потому, что сборка BuildTargetIPK кладёт рантайм внутрь него.
var ownRuntimePackages = map[string]bool{RuntimePackageName: true, "awg-manager": true}

// foreignPackageFiles — файлы, по которым пакет Entware узнаётся как firc.
var foreignPackageFiles = map[string]bool{
	DefaultFircdBinPath: true, fircdBinLink: true,
	DefaultTunvlessBinPath: true, "/opt/bin/tunvless": true,
	DefaultInitScriptPath:       true,
	DefaultNDMNetfilterHookPath: true,
	DefaultNDMIfLayerHookPath:   true,
}

// ForeignDetector ищет сторонний firc под корнем root ("" — сам роутер).
// Нулевой указатель — «стороннего нет»: так работают операторы с отдельным
// каталогом конфигурации (тесты, разработка).
type ForeignDetector struct {
	root string

	mu     sync.Mutex
	cached bool
	key    [6]fileStamp
	result *ForeignFirc
}

// NewForeignDetector создаёт детектор для установки под корнем root.
func NewForeignDetector(root string) *ForeignDetector {
	if root == "/" {
		root = ""
	}
	return &ForeignDetector{root: root}
}

func (d *ForeignDetector) path(p string) string {
	return filepath.Join(d.root, p)
}

type fileStamp struct {
	exists bool
	mtime  int64
	size   int64
	mode   os.FileMode
}

// foreignStampWindow — насколько свежую отметку времени кэш не принимает:
// повторная запись в тот же такт часов ФС отметку не меняет.
const foreignStampWindow = 2 * time.Second

func stampOf(info os.FileInfo, err error) fileStamp {
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{exists: true, mtime: info.ModTime().UnixNano(), size: info.Size(), mode: info.Mode()}
}

// Detect возвращает сторонний firc или nil. Ошибка значит, что владельца файлов
// установить не удалось: вызывающие обязаны считать это отказом. Зовётся на
// каждом запросе к fircd, поэтому списки пакетов перечитываются только при
// смене каталога opkg, журнала рантайма или самих бинарей.
func (d *ForeignDetector) Detect() (*ForeignFirc, error) {
	if d == nil {
		return nil, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	key := [6]fileStamp{
		stampOf(os.Stat(d.path(opkgInfoDir))),
		stampOf(os.Stat(d.path(runtimeOperationFile))),
		stampOf(os.Lstat(d.path(DefaultFircdBinPath))),
		stampOf(os.Stat(d.path(DefaultFircdBinPath))),
		stampOf(os.Lstat(d.path(fircdBinLink))),
		stampOf(os.Stat(d.path(fircdBinLink))),
	}
	if d.cached && d.key == key {
		return d.result, nil
	}
	d.cached = false
	found, err := d.scan()
	if err != nil {
		return nil, fmt.Errorf("firc: проверка владельца файлов firc: %w", err)
	}
	now := time.Now().UnixNano()
	for _, s := range key {
		if age := now - s.mtime; s.exists && age < int64(foreignStampWindow) && age > -int64(foreignStampWindow) {
			return found, nil
		}
	}
	d.cached, d.key, d.result = true, key, found
	return found, nil
}

func (d *ForeignDetector) scan() (*ForeignFirc, error) {
	infoDir := d.path(opkgInfoDir)
	entries, err := os.ReadDir(infoDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	ownFircd := false
	for _, entry := range entries {
		pkg, isList := strings.CutSuffix(entry.Name(), ".list")
		if !isList {
			continue
		}
		data, err := os.ReadFile(filepath.Join(infoDir, entry.Name()))
		if os.IsNotExist(err) {
			continue // пакет сняли между чтением каталога и списка
		}
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			file, _, _ := strings.Cut(line, "\t") // новый opkg дописывает режим файла
			file = strings.TrimSpace(file)
			if ownRuntimePackages[pkg] {
				ownFircd = ownFircd || file == DefaultFircdBinPath
			} else if foreignPackageFiles[file] {
				return &ForeignFirc{Package: pkg}, nil
			}
		}
	}
	return d.foreignBinary(ownFircd || d.ownOperationPending())
}

// ownOperationPending — прерванная операция нашего пакета: бинарь уже (или ещё)
// на месте, а списка файлов у opkg нет. Это наш рантайм, его чинит панель.
func (d *ForeignDetector) ownOperationPending() bool {
	raw, err := os.ReadFile(d.path(runtimeOperationFile))
	if err != nil {
		return false
	}
	var op runtimeOperation
	return json.Unmarshal(raw, &op) == nil && op.Pending
}

// foreignBinary ищет fircd, который не наш рантайм. Наш — это /opt/sbin/fircd
// из нашего пакета и ссылка на него в /opt/bin; всё остальное чужое.
func (d *ForeignDetector) foreignBinary(ownsSbin bool) (*ForeignFirc, error) {
	ours := ""
	if ownsSbin {
		real, err := filepath.EvalSymlinks(d.path(DefaultFircdBinPath))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		ours = real
	}
	for _, p := range []string{DefaultFircdBinPath, fircdBinLink} {
		real, err := filepath.EvalSymlinks(d.path(p))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if real != ours {
			return &ForeignFirc{Binary: p}, nil
		}
	}
	return nil, nil
}

// describe дополняет находку адресом веб-интерфейса из конфига firc. Порт по
// умолчанию не угадываем: без явных enabled и port ссылку не показываем.
func (d *ForeignDetector) describe(f *ForeignFirc) *ForeignFirc {
	out := *f
	raw, err := os.ReadFile(d.path(DefaultConfigFilePath))
	if err != nil {
		return &out
	}
	var conf struct {
		App struct {
			HTTPWeb struct {
				Enabled bool `yaml:"enabled"`
				Host    struct {
					Address string `yaml:"address"`
					Port    int    `yaml:"port"`
				} `yaml:"host"`
			} `yaml:"httpWeb"`
		} `yaml:"app"`
	}
	if yaml.Unmarshal(raw, &conf) != nil || !conf.App.HTTPWeb.Enabled {
		return &out
	}
	port := conf.App.HTTPWeb.Host.Port
	if port < 1 || port > 65535 {
		return &out
	}
	switch host := strings.Trim(strings.TrimSpace(conf.App.HTTPWeb.Host.Address), "[]"); host {
	case "127.0.0.1", "::1", "localhost":
		return &out // с другого устройства не открыть
	case "", "0.0.0.0", "::":
		out.WebPort = port
	default:
		out.WebPort, out.WebHost = port, host
	}
	return &out
}

// CheckNotForeign — отказ, если FIRC на роутере не наш (или владельца не узнать).
func (o *Operator) CheckNotForeign() error {
	if o == nil {
		return nil
	}
	found, err := o.foreign.Detect()
	if err != nil {
		return err
	}
	if found != nil {
		return ErrForeignFirc
	}
	return nil
}

// ForeignFirc описывает сторонний firc или возвращает nil.
func (o *Operator) ForeignFirc() *ForeignFirc {
	if o == nil {
		return nil
	}
	found, err := o.foreign.Detect()
	if err != nil || found == nil {
		return nil
	}
	return o.foreign.describe(found)
}
