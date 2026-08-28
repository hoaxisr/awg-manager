// Package install — установка бинарей прокси и статус установки.
//
// Перенос механизма из умирающих internal/wdtt и internal/freeturn: пины
// сборок, version-файлы, сверка бинарей с пином и семь полей install-статуса,
// которыми живёт фронт (полоса состояния бинарей, гейт мастера на арке без
// сервера, часы роутера в журнале).
package install

// BinarySpec — пин одного бинаря: версия, адрес, SHA256 и размер, вшитые в
// эту сборку awg-manager. Модель доверия та же, что у sing-box-установщика:
// скомпрометированный источник загрузки не сможет подсунуть подменённый
// бинарь, который awg-manager всё равно поставит.
type BinarySpec struct {
	Version string
	URL     string
	SHA256  string
	Size    int64 // bytes; download hard-cap = Size + slack
}

// ArchSpecs — пара клиент+сервер для одной архитектуры роутера.
type ArchSpecs struct {
	Client BinarySpec
	Server BinarySpec
}

// serverSupported — есть ли для этой арки собираемый сервер.
func (s *ArchSpecs) serverSupported() bool { return s != nil && s.Server.URL != "" }

// ── wdtt ─────────────────────────────────────────────────────────

const WdttPinnedClientVersion = "1.4.0-3"
const WdttPinnedServerVersion = "1.4.0-4"

// Порядок выпуска обоих бинарей: тег в форке hoaxisr/proxy-turn-vk-android →
// сборка в GitHub Actions → релиз с checksums.txt → зеркало repo.hoaxisr.ru
// забирает релиз само → пины ниже обновляются из checksums.txt:
// scripts/update-wdtt-pins.py --client-tag ... --server-tag ...

// wdttReleaseBase — прод-доставка клиента с зеркала (паритет с freeturn).
//
// Каталог client/: там лежат сборки НАШЕГО конвейера (тег awgm-client-* →
// GitHub Actions → релиз → зеркало). Пин смотрел на соседний /wt/1.4.4-awgm/ —
// сборку от 10.08 из upstream v1.4.0 скриптом build-wdtt-client.sh, ДО того как
// в клиенте появилась обвязка управляющего протокола. Проба --awgm-protocol на
// ней падает, и гейт procres.Gate не пускал ни один инстанс (стенд 2026-08-28).
const wdttReleaseBase = "http://repo.hoaxisr.ru/wt/client/" + WdttPinnedClientVersion + "/"

// wdttServerReleaseBase — wdtt-server из форка (монолит с Keenetic-флагами).
// Арка больше не разводится: 1.4.0-3 собрана для всех трёх, и arm64-сборка
// прежнего пина (1.4.4-awgm) обвязку протокола тоже не несла.
const wdttServerReleaseBase = "http://repo.hoaxisr.ru/wt/server/" + WdttPinnedServerVersion + "/"

// WdttEmbeddedBinaries связывает арку сборки awg-manager с пинами wdtt.
var WdttEmbeddedBinaries = map[string]ArchSpecs{
	"aarch64-3.10": {
		Client: BinarySpec{
			Version: WdttPinnedClientVersion, URL: wdttReleaseBase + "wt-client-linux-arm64",
			SHA256: "ed627553a8b970ab50edfdeb7a7fe35811387b6e19df901bb160f4db46d42367", Size: 15401122,
		},
		Server: BinarySpec{
			Version: WdttPinnedServerVersion, URL: wdttServerReleaseBase + "wdtt-server-linux-arm64",
			SHA256: "2473b1e0212f9731cb204ac4885390e295d9f9b3de1d0828b461ddbdc2bea45e", Size: 7995576,
		},
	},
	"mipsel-3.4": {
		Client: BinarySpec{
			Version: WdttPinnedClientVersion, URL: wdttReleaseBase + "wt-client-linux-mipsle-softfloat",
			SHA256: "b98f366a8f669142cca64066a592a9fa2aa92adcbf42207b5f540ba5da659ce1", Size: 17694913,
		},
		Server: BinarySpec{
			Version: WdttPinnedServerVersion, URL: wdttServerReleaseBase + "wdtt-server-linux-mipsle-softfloat",
			SHA256: "32a762ec05c9c68abd82aa659a9fa545b4bf7655a6aeb410a1ab57bb06065b5f", Size: 9502935,
		},
	},
	"mips-3.4": {
		Client: BinarySpec{
			Version: WdttPinnedClientVersion, URL: wdttReleaseBase + "wt-client-linux-mips-softfloat",
			SHA256: "af34a9ce0a7386878cc77b7855d555fb41ff1d99f521887efe21d5eb18f00250", Size: 17694913,
		},
		Server: BinarySpec{
			Version: WdttPinnedServerVersion, URL: wdttServerReleaseBase + "wdtt-server-linux-mips-softfloat",
			SHA256: "e6a06aa9f57fb02c485e7bd23da6b2f106f5835cae7a74d39f8f807d4f764956", Size: 9502935,
		},
	},
}

// ── freeturn ─────────────────────────────────────────────────────

// FreeTurnPinnedVersion — релиз free-turn-proxy, который ставит эта сборка.
// Порядок бампа: обновить константу, URL, SHA256 (из checksums.txt релиза) и
// размеры ниже.
const FreeTurnPinnedVersion = "2.1.1-2"

// freeturnReleaseBase — прод-доставка с зеркала (паритет с
// internal/singbox/installer/embedded.go — GitHub из RU у части пользователей
// недоступен). Канонический источник сборки:
// https://github.com/hoaxisr/free-turn-proxy релиз v<FreeTurnPinnedVersion>.
const freeturnReleaseBase = "http://repo.hoaxisr.ru/ft/" + FreeTurnPinnedVersion + "/"

// FreeTurnEmbeddedBinaries связывает арку сборки awg-manager с пинами
// freeturn. SHA256/Size — из checksums.txt релиза hoaxisr/free-turn-proxy
// v<FreeTurnPinnedVersion> (ветка awg поверх upstream v2.1.1). Источник
// истины — checksums.txt из GitHub-релиза: локальная сборка ему не равна
// (свой тулчейн + встроенная VCS-ревизия), на зеркало кладём ровно артефакты
// релиза.
var FreeTurnEmbeddedBinaries = map[string]ArchSpecs{
	"aarch64-3.10": {
		Client: BinarySpec{Version: FreeTurnPinnedVersion, URL: freeturnReleaseBase + "ft-client-linux-arm64", SHA256: "43ad01739049a2a0bcf775a72c1385eadd18bf49ad08f346091cf75f530582d1", Size: 14942370},
		Server: BinarySpec{Version: FreeTurnPinnedVersion, URL: freeturnReleaseBase + "ft-server-linux-arm64", SHA256: "e174b5a86764f30ca45d21346bbdd21ae105325e0ad5eec6aca8707919489e81", Size: 6291618},
	},
	"mipsel-3.4": {
		Client: BinarySpec{Version: FreeTurnPinnedVersion, URL: freeturnReleaseBase + "ft-client-linux-mipsle-softfloat", SHA256: "78ec5f3dab8e8c5c5b71cc8e0de306f8c389bd0c962258ecc287b4b16a15f4e7", Size: 16842945},
		Server: BinarySpec{Version: FreeTurnPinnedVersion, URL: freeturnReleaseBase + "ft-server-linux-mipsle-softfloat", SHA256: "7858eead802128cac0afb132dbbb7a64a95be3304fecc3acece69cb3c55163e8", Size: 7143617},
	},
	"mips-3.4": {
		Client: BinarySpec{Version: FreeTurnPinnedVersion, URL: freeturnReleaseBase + "ft-client-linux-mips-softfloat", SHA256: "84cf1d9325ca71e57d471ee89c41957738bb9177caf6ec0984f95d0911806f60", Size: 16842945},
		Server: BinarySpec{Version: FreeTurnPinnedVersion, URL: freeturnReleaseBase + "ft-server-linux-mips-softfloat", SHA256: "3f43e69667f24121d62d29919c6196f9f8ac927ec97a92270b5a624d0cb849fd", Size: 7143617},
	},
}
