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
const WdttPinnedServerVersion = "1.4.0-5"

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
			SHA256: "ce9264b96c8b7d330dde5add7d1d4e9a43b22a56536b1720b2cfe519a5a24e90", Size: 7995576,
		},
	},
	"mipsel-3.4": {
		Client: BinarySpec{
			Version: WdttPinnedClientVersion, URL: wdttReleaseBase + "wt-client-linux-mipsle-softfloat",
			SHA256: "b98f366a8f669142cca64066a592a9fa2aa92adcbf42207b5f540ba5da659ce1", Size: 17694913,
		},
		Server: BinarySpec{
			Version: WdttPinnedServerVersion, URL: wdttServerReleaseBase + "wdtt-server-linux-mipsle-softfloat",
			SHA256: "a681e666e119507708668ef9381d51c22e92353dea733e433dec60771b81535f", Size: 9502935,
		},
	},
	"mips-3.4": {
		Client: BinarySpec{
			Version: WdttPinnedClientVersion, URL: wdttReleaseBase + "wt-client-linux-mips-softfloat",
			SHA256: "af34a9ce0a7386878cc77b7855d555fb41ff1d99f521887efe21d5eb18f00250", Size: 17694913,
		},
		Server: BinarySpec{
			Version: WdttPinnedServerVersion, URL: wdttServerReleaseBase + "wdtt-server-linux-mips-softfloat",
			SHA256: "9a53ba74051d1d1f060076ae179295e7fe57c1828771e00f1da95b334ecb9fcf", Size: 9502935,
		},
	},
}

// ── freeturn ─────────────────────────────────────────────────────

// FreeTurnPinnedVersion — релиз free-turn-proxy, который ставит эта сборка.
// Порядок бампа: обновить константу, URL, SHA256 (из checksums.txt релиза) и
// размеры ниже.
const FreeTurnPinnedVersion = "4.0.1-1"

// freeturnReleaseBase — прод-доставка с зеркала (паритет с
// internal/singbox/installer/embedded.go — GitHub из RU у части пользователей
// недоступен). Канонический источник сборки:
// https://github.com/hoaxisr/free-turn-proxy релиз v<FreeTurnPinnedVersion>.
const freeturnReleaseBase = "http://repo.hoaxisr.ru/ft/" + FreeTurnPinnedVersion + "/"

// FreeTurnEmbeddedBinaries связывает арку сборки awg-manager с пинами
// freeturn. SHA256/Size — из checksums.txt релиза hoaxisr/free-turn-proxy
// v<FreeTurnPinnedVersion> (ветка awg поверх upstream v4.0.1). Источник
// истины — checksums.txt из GitHub-релиза: локальная сборка ему не равна
// (свой тулчейн + встроенная VCS-ревизия), на зеркало кладём ровно артефакты
// релиза.
var FreeTurnEmbeddedBinaries = map[string]ArchSpecs{
	"aarch64-3.10": {
		Client: BinarySpec{Version: FreeTurnPinnedVersion, URL: freeturnReleaseBase + "ft-client-linux-arm64", SHA256: "4eee1f1da11899d5326f2a6d1bb3f32d82e1c6e0107daed0cd76e38622a5f181", Size: 15204514},
		Server: BinarySpec{Version: FreeTurnPinnedVersion, URL: freeturnReleaseBase + "ft-server-linux-arm64", SHA256: "92945229b246d3badf0bda2d8d7d2e65d56d4b7a2a3c94719cfecc58ac9c92d2", Size: 6881442},
	},
	"mipsel-3.4": {
		Client: BinarySpec{Version: FreeTurnPinnedVersion, URL: freeturnReleaseBase + "ft-client-linux-mipsle-softfloat", SHA256: "9db240aab211a74f2da01058c3ccc8caf07bb02ebc6ebeb2b305d7ae2e36e508", Size: 17301697},
		Server: BinarySpec{Version: FreeTurnPinnedVersion, URL: freeturnReleaseBase + "ft-server-linux-mipsle-softfloat", SHA256: "f5fc6b9bbbb04407e6a83bb23df94d251d1a98b458fef9f1cc97b1c6754c3759", Size: 7798977},
	},
	"mips-3.4": {
		Client: BinarySpec{Version: FreeTurnPinnedVersion, URL: freeturnReleaseBase + "ft-client-linux-mips-softfloat", SHA256: "80224d6c4a3f40e45ef483dc1cd0b720db74d5ba592dc2945eea7d065e334b0d", Size: 17301697},
		Server: BinarySpec{Version: FreeTurnPinnedVersion, URL: freeturnReleaseBase + "ft-server-linux-mips-softfloat", SHA256: "7e318efc146030eae1c120d3fb5c691c7201ea9f1decf5341c2f631a8e173da0", Size: 7798977},
	},
}

// ── openflux ─────────────────────────────────────────────────────

// OpenFluxPinnedVersion — релиз форка OpenFlux с обвязкой awgmproto, который
// ставит эта сборка. Порядок бампа — паритет с freeturn: обновить константу,
// URL, SHA256 (из checksums.txt релиза) и размеры ниже.
const OpenFluxPinnedVersion = "1.0.0-1"

// openFluxReleaseBase — прод-доставка с зеркала. Канонический источник сборки:
// релиз форка OpenFlux с обвязкой управляющего протокола (upstream —
// github.com/p1neappleXpress/OpenFlux; бинарь без обвязки не проходит пробу
// --awgm-protocol, гейт procres.Gate его не пускает).
const openFluxReleaseBase = "http://repo.hoaxisr.ru/of/" + OpenFluxPinnedVersion + "/"

// OpenFluxEmbeddedBinaries связывает арку сборки awg-manager с пинами
// OpenFlux.
//
// ВАЖНО: SHA256 и Size пусты до первого релиза форка — их заполняют из
// checksums.txt релиза (по образцу scripts/update-wdtt-pins.py). childproc
// отказывается ставить бинарь с пустым ожидаемым SHA256, поэтому до заполнения
// подсистема отвечает «пина нет»: installInfo отдаёт installUnavailable, а
// инстансы поднимаются на вручную положенных в /opt/bin бинарях — их гейт
// проверяет пробой протокола, а не пином.
var OpenFluxEmbeddedBinaries = map[string]ArchSpecs{
	"aarch64-3.10": {
		Client: BinarySpec{Version: OpenFluxPinnedVersion, URL: openFluxReleaseBase + "openflux-client-linux-arm64"},
		Server: BinarySpec{Version: OpenFluxPinnedVersion, URL: openFluxReleaseBase + "openflux-server-linux-arm64"},
	},
	"mipsel-3.4": {
		Client: BinarySpec{Version: OpenFluxPinnedVersion, URL: openFluxReleaseBase + "openflux-client-linux-mipsle-softfloat"},
		Server: BinarySpec{Version: OpenFluxPinnedVersion, URL: openFluxReleaseBase + "openflux-server-linux-mipsle-softfloat"},
	},
	"mips-3.4": {
		Client: BinarySpec{Version: OpenFluxPinnedVersion, URL: openFluxReleaseBase + "openflux-client-linux-mips-softfloat"},
		Server: BinarySpec{Version: OpenFluxPinnedVersion, URL: openFluxReleaseBase + "openflux-server-linux-mips-softfloat"},
	},
}

// ── wg-obfuscator ────────────────────────────────────────────────

// Теги в hoaxisr/wg-obfuscator: awgm-phobos-v<ver> (ветка phobos — форк
// Ground-Zerro/Phobos), awgm-clusterm-v<ver> (ветка clusterm — upstream).
// Зеркало repo-sync-thirdparty кладёт ассеты в obf/<comp>/<ver>/.
const (
	ObfPhobosPinnedVersion   = "20260906-1"
	ObfClusterMPinnedVersion = "1.6-1"
	obfPhobosBase            = "http://repo.hoaxisr.ru/obf/phobos/" + ObfPhobosPinnedVersion + "/"
	obfClusterMBase          = "http://repo.hoaxisr.ru/obf/clusterm/" + ObfClusterMPinnedVersion + "/"
)

// wg-obfuscator: один бинарь на арку, серверной половины нет (Server пустой →
// serverSupported()=false, Install/binariesMatchSpecs её пропускают).
var ObfPhobosEmbeddedBinaries = map[string]ArchSpecs{
	"aarch64-3.10": {Client: BinarySpec{Version: ObfPhobosPinnedVersion, URL: obfPhobosBase + "wg-obfuscator-phobos-linux-arm64", SHA256: "611c3f2001e6f66afe8168db9737679f6640a620bafd09688ecd5f30279cfd43", Size: 170568}},
	"mipsel-3.4":   {Client: BinarySpec{Version: ObfPhobosPinnedVersion, URL: obfPhobosBase + "wg-obfuscator-phobos-linux-mipsle-softfloat", SHA256: "bd59e224664d39cec3877a98f3ba32f24e9ab81cb1ac2752505735101c5a4361", Size: 228492}},
	"mips-3.4":     {Client: BinarySpec{Version: ObfPhobosPinnedVersion, URL: obfPhobosBase + "wg-obfuscator-phobos-linux-mips-softfloat", SHA256: "8c5cc9792cbd2d772e5e30fcf88c4e7150e11a549a67ce38cca70668f01e159b", Size: 228556}},
}

var ObfClusterMEmbeddedBinaries = map[string]ArchSpecs{
	"aarch64-3.10": {Client: BinarySpec{Version: ObfClusterMPinnedVersion, URL: obfClusterMBase + "wg-obfuscator-clusterm-linux-arm64", SHA256: "de21cb808f56d88e2ddba803b8530b05e2a1f4f007fea5dbe6715806565eb46a", Size: 129576}},
	"mipsel-3.4":   {Client: BinarySpec{Version: ObfClusterMPinnedVersion, URL: obfClusterMBase + "wg-obfuscator-clusterm-linux-mipsle-softfloat", SHA256: "50fc63fd74dc367dee644c70e853d7534f2a78f6457d9a372bbc78d3946be604", Size: 192064}},
	"mips-3.4":     {Client: BinarySpec{Version: ObfClusterMPinnedVersion, URL: obfClusterMBase + "wg-obfuscator-clusterm-linux-mips-softfloat", SHA256: "44531c7b0d3785cfe15925430ef758dd1d34f8a7280f7bb3dd2fbce039a0d0ea", Size: 192064}},
}
