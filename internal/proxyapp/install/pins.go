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

const WdttPinnedClientVersion = "1.4.4-awgm"
const WdttPinnedServerVersion = "1.4.4-awgm"

// Порядок выпуска обоих бинарей: тег в форке hoaxisr/proxy-turn-vk-android →
// сборка в GitHub Actions → релиз с checksums.txt → зеркало repo.hoaxisr.ru
// забирает релиз само → пины ниже обновляются из checksums.txt:
// scripts/update-wdtt-pins.py --client-tag ... --server-tag ...

// wdttReleaseBase — прод-доставка клиента с зеркала (паритет с freeturn).
const wdttReleaseBase = "http://repo.hoaxisr.ru/wt/" + WdttPinnedClientVersion + "/"

// wdttServerReleaseBase — wdtt-server из форка (монолит с Keenetic-флагами).
const wdttServerReleaseBase = "http://repo.hoaxisr.ru/wt/server/" + WdttPinnedServerVersion + "/"

// WdttPinnedServerVersionMIPS — серверная сборка для mips/mipsel. Версия НИЖЕ
// arm64-й намеренно: под 1.4.4-awgm серверные бинари для mips на зеркало не
// выкладывались, а 1.4.0-3 собрана для всех трёх арок. Пин снят с
// checksums.txt зеркала и перепроверен локально.
const WdttPinnedServerVersionMIPS = "1.4.0-3"

const wdttServerReleaseBaseMIPS = "http://repo.hoaxisr.ru/wt/server/" + WdttPinnedServerVersionMIPS + "/"

// WdttEmbeddedBinaries связывает арку сборки awg-manager с пинами wdtt.
var WdttEmbeddedBinaries = map[string]ArchSpecs{
	"aarch64-3.10": {
		Client: BinarySpec{
			Version: WdttPinnedClientVersion, URL: wdttReleaseBase + "wt-client-linux-arm64",
			SHA256: "6f82bfd0b5851b1c61398d80ea4665575ba570c5dd641997194b25aef17f6e83", Size: 15401122,
		},
		Server: BinarySpec{
			Version: WdttPinnedServerVersion, URL: wdttServerReleaseBase + "wdtt-server-linux-arm64",
			SHA256: "b639505b9952485bc16e9e3d43d6503975a878b0b18aba3fa5269953b61fd000", Size: 8126626,
		},
	},
	"mipsel-3.4": {
		Client: BinarySpec{
			Version: WdttPinnedClientVersion, URL: wdttReleaseBase + "wt-client-linux-mipsle-softfloat",
			SHA256: "0af429515d65f7f844c3d24f0ec052c6b27cb65f6a9ef70e6ebdeb9f39782b7d", Size: 17563841,
		},
		Server: BinarySpec{
			Version: WdttPinnedServerVersionMIPS, URL: wdttServerReleaseBaseMIPS + "wdtt-server-linux-mipsle-softfloat",
			SHA256: "7024c1da12bae2f7677654da6450946cf856e4e700c3b61b29d203fbdc6cac5e", Size: 9437399,
		},
	},
	"mips-3.4": {
		Client: BinarySpec{
			Version: WdttPinnedClientVersion, URL: wdttReleaseBase + "wt-client-linux-mips-softfloat",
			SHA256: "8b4d2d838c696f91b771507c2992ba62d9b1f2993fad32ef396d5b53b976906e", Size: 17563841,
		},
		Server: BinarySpec{
			Version: WdttPinnedServerVersionMIPS, URL: wdttServerReleaseBaseMIPS + "wdtt-server-linux-mips-softfloat",
			SHA256: "100f7459d7e53d4e04716c0b8fefa6c71e7b7d5d5f382c77c8992f374f14ba06", Size: 9437399,
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
