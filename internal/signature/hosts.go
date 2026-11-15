// Adapted from payloadGen (MIT) — https://github.com/Sketchystan1/payloadGen
package signature

import (
	"errors"
	"fmt"
	mrand "math/rand"
	"strings"
)

const (
	HostPresetDefault = "default"
	HostPresetRU      = "ru_whitelist"
	HostPresetCDN     = "cdn"
	HostPresetCustom  = "custom"

	MaxDomainLen     = 128
	MaxDomainListLen = 64
)

var (
	ErrInvalidDomain       = errors.New("invalid domain")
	ErrInvalidDomainPreset = errors.New("invalid domain preset")
)

var ruWhitelistPool = []string{
	"gosuslugi.ru", "esia.gosuslugi.ru", "vk.com", "yandex.ru", "ya.ru",
	"sberbank.ru", "online.sberbank.ru", "tbank.ru", "vtb.ru", "alfabank.ru",
	"gazprombank.ru", "ozon.ru", "wildberries.ru", "mail.ru", "dzen.ru",
	"rutube.ru", "kinopoisk.ru", "avito.ru", "2gis.ru", "mos.ru",
	"nalog.ru", "rbc.ru", "ria.ru", "lenta.ru", "kp.ru",
	"hh.ru", "habr.com", "pikabu.ru", "ok.ru", "rustore.ru",
	"ivi.ru", "okko.tv", "mts.ru", "megafon.ru", "beeline.ru", "rostelecom.ru",
}

var cdnHostPool = []string{
	"cloudflare.com", "cloudfront.net", "fastly.net", "global.fastly.net",
	"akamaiedge.net", "akamaihd.net", "cdn.jsdelivr.net", "unpkg.com",
	"msedge.net", "azureedge.net", "apple.com", "icloud.com",
	"cdn-apple.com", "mzstatic.com", "github.com", "raw.githubusercontent.com",
	"google.com", "googleapis.com", "bunny.net", "b-cdn.net",
	"gcore.com", "cdn77.com",
}

// hostPool — SNI/имена хостов для профилей. Первая часть (108 записей) —
// DOMAIN_POOL из payloadGen (порт-спека §6, порядок = вес: чем выше, тем
// чаще, дубли google.com/apple.com/microsoft.com/github.com сохранены как
// вес), затем веб-хосты прежнего generate_hostpools.go, которых там ещё нет.
// Только веб: STUN/TURN и SIP-инфраструктура из старых попротокольных пулов
// сюда не входит (в h3 ClientHello такое имя выдаёт инструмент), STUN берёт
// хосты из своих таблиц провайдеров.
var hostPool = []string{
	"google.com", "amazon.com", "reddit.com", "github.com", "mozilla.org", "microsoft.com",
	"apple.com", "cloudflare.com", "bing.com", "adobe.com", "stackoverflow.com", "office.com",
	"dropbox.com", "zoom.us", "spotify.com", "imdb.com", "wikipedia.org", "yandex.ru",
	"ozon.ru", "vk.com", "google.com", "gismeteo.ru", "mail.ru", "kinopoisk.ru",
	"pinterest.com", "dzen.ru", "rutube.ru", "gdz.ru", "apple.com", "rbc.ru",
	"wildberries.ru", "asna.ru", "ya.ru", "vidal.ru", "banki.ru", "sberbank.ru",
	"ria.ru", "tbank.ru", "championat.com", "fandom.com", "rambler.ru", "kp.ru",
	"dns-shop.ru", "russianfood.com", "eapteka.ru", "chatgpt.com", "avito.ru", "2gis.ru",
	"cbr.ru", "ivi.ru", "gosuslugi.ru", "pozdravok.com", "microsoft.com", "kino-teatr.ru",
	"consultant.ru", "lenta.ru", "auto.ru", "smclinic.ru", "steampowered.com", "funpay.com",
	"rustore.ru", "okko.tv", "domclick.ru", "sports.ru", "cian.ru", "drom.ru",
	"investing.com", "gastronom.ru", "24smi.org", "sravni.ru", "iamcook.ru", "vseinstrumenti.ru",
	"planetazdorovo.ru", "wiktionary.org", "soccer365.ru", "aviasales.ru", "sovcombank.ru", "irecommend.ru",
	"sportbox.ru", "freepik.com", "gemotest.ru", "invitro.ru", "github.com", "tutu.ru",
	"skysmart.ru", "vtb.ru", "goldapple.ru", "ok.ru", "kareliameteo.ru", "pikabu.ru",
	"deepl.com", "meteoinfo.ru", "tripadvisor.ru", "iz.ru", "megapteka.ru", "apteka.ru",
	"reverso.net", "hh.ru", "primpogoda.ru", "habr.com", "gdz-raketa.ru", "uchi.ru",
	"lamoda.ru", "deti-online.com", "promokodi.net", "gorzdrav.org", "uiscom.ru", "primbank.ru",

	"yandex.net", "yastatic.net", "s3.yandex.net", "storage.yandexcloud.net", "cloud.yandex.ru", "music.yandex.ru",
	"mycdn.me", "vk-cdn.net", "userapi.com", "imgsmail.ru", "cdn.mail.ru", "cdn1.ozone.ru",
	"wbstatic.net", "rt.ru", "sber.ru", "sbp.ru", "raiffeisen.ru", "alfabank.ru",
	"gazprombank.ru", "rosbank.ru", "kaspersky.ru", "kaspersky.com", "drweb.ru", "selectel.ru",
	"selectel.com", "timeweb.cloud", "timeweb.com", "reg.ru", "beget.com", "mchost.ru",
	"nic.ru", "dataline.ru", "mts.ru", "beeline.ru", "megafon.ru", "rostelecom.ru",
	"tele2.ru", "mvideo.ru", "eldorado.ru", "citilink.ru", "sportmaster.ru", "detmir.ru",
	"sbermegamarket.ru", "mos.ru", "nalog.ru", "pfr.gov.ru", "roscosmos.ru", "premier.one",
	"more.tv", "1tv.ru", "ntv.ru", "russia.tv", "tass.ru", "gazeta.ru",
	"superjob.ru", "livejournal.com", "gcore.com", "api.gcore.com", "cdn.gcore.com", "g.gcdn.co",
	"gcdn.co", "bunny.net", "b-cdn.net", "storage.bunnycdn.com", "cdn77.com", "rsc.cdn77.org",
	"fastly.net", "a.ssl.fastly.net", "global.fastly.net", "fastlylabs.com", "a248.e.akamai.net", "akamaiedge.net",
	"akamaihd.net", "akamaistream.net", "edgekey.net", "akam.net", "cloudfront.net", "d1.awsstatic.com",
	"d2.awsstatic.com", "s3.amazonaws.com", "msedge.net", "cdn.office.net", "azureedge.net", "azure.microsoft.com",
	"live.com", "outlook.com", "hotmail.com", "xbox.com", "xboxlive.com", "onedrive.live.com",
	"trafficmanager.net", "icloud.com", "cdn-apple.com", "mzstatic.com", "appleid.apple.com", "limelight.com",
	"llnwd.net", "edg.io", "highwinds.com", "stackpathdns.com", "cachefly.net", "imperva.com",
	"objects.githubusercontent.com", "raw.githubusercontent.com", "codeload.github.com", "github.githubassets.com", "avatars.githubusercontent.com", "releases.githubusercontent.com",
	"gitlab.com", "cdn.jsdelivr.net", "unpkg.com", "registry.npmjs.org", "pypi.org", "files.pythonhosted.org",
	"archive.ubuntu.com", "security.ubuntu.com", "deb.debian.org", "steamstatic.com", "steamcontent.com", "epicgames.com",
	"ea.com", "battle.net", "blizzard.com", "ubisoft.com", "riotgames.com", "leagueoflegends.com",
	"scdn.co", "heads-ak.spotify.com", "jtvnw.net", "twitchsvc.net", "upload.wikimedia.org", "wikimedia.org",
	"wikidata.org", "hetzner.com", "hetzner.de", "hetzner.cloud", "your-server.de", "ovhcloud.com",
	"ovh.net", "ovh.com", "digitalocean.com", "dropboxstatic.com", "dropboxapi.com", "notion.so",
	"notionusercontent.com", "valve.net", "linode.com", "tencentcs.com", "tencent.com", "myqcloud.com",
	"qpic.cn", "alicdn.com", "aliyuncs.com", "alibabacloud.com", "huaweicloud.com", "hwcdn.net",
	"baidu.com", "bdstatic.com", "bceloss.com", "ren.tv", "tvc.ru", "5-tv.ru",
	"online.sberbank.ru", "vtb24.ru", "otkritie.ru", "rshb.ru", "pochtabank.ru", "bspb.ru",
	"drweb.com", "esia.gosuslugi.ru", "epgu.gosuslugi.ru", "spaceweb.ru", "sweb.ru", "ihc.ru",
	"fastvps.ru", "letoile.ru", "technopark.ru", "nix.ru", "aliexpress.ru", "joom.com",
	"kommersant.ru", "rabota.ru", "rg.ru", "mk.ru", "izvestia.ru", "vedomosti.ru",
	"maps.yandex.ru", "matchtv.ru", "vc.ru", "spark.ru", "ruvds.com", "vdsina.ru",
	"gcorelabs.com", "aws.amazon.com", "incapsula.com", "sucuri.net", "bitbucket.org", "packages.ubuntu.com",
	"ftp.debian.org", "launchpad.net", "snapcraft.io", "alpinelinux.org", "archlinux.org", "centos.org",
	"fedoraproject.org", "steamcdn-a.akamaihd.net", "store.steampowered.com", "commons.wikimedia.org", "gra-g1.ovh.net", "do.co",
	"vultr.com", "zmtr.cn", "docker.com", "hub.docker.com", "registry-1.docker.io", "quay.io",
	"ghcr.io", "jetbrains.com", "plugins.jetbrains.com", "download.jetbrains.com",
}

// pickHost — выбор, взвешенный по рангу (payloadGen pickWeightedRankedDomain):
// вес i-й позиции = len-i.
func pickHost(r *mrand.Rand) string {
	n := len(hostPool)
	total := n * (n + 1) / 2
	roll := r.Intn(total)
	for i, h := range hostPool {
		roll -= n - i
		if roll < 0 {
			return h
		}
	}
	return hostPool[n-1]
}

func hostPoolHas(h string) bool {
	for _, x := range hostPool {
		if x == h {
			return true
		}
	}
	return false
}

// CanonicalHostPreset нормализует ключ пула доменов (default, ru_whitelist,
// cdn, custom) или возвращает "" для неизвестного значения.
func CanonicalHostPreset(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", HostPresetDefault:
		return HostPresetDefault
	case HostPresetRU, "ru", "whitelist":
		return HostPresetRU
	case HostPresetCDN:
		return HostPresetCDN
	case HostPresetCustom:
		return HostPresetCustom
	default:
		return ""
	}
}

// PresetDomains возвращает копию списка доменов для встроенного пресета.
func PresetDomains(preset string) ([]string, bool) {
	var src []string
	switch CanonicalHostPreset(preset) {
	case HostPresetRU:
		src = ruWhitelistPool
	case HostPresetCDN:
		src = cdnHostPool
	case HostPresetDefault:
		src = hostPool
	default:
		return nil, false
	}
	out := make([]string, len(src))
	copy(out, src)
	return out, true
}

// ValidateDomain проверяет и нормализует доменное имя для подстановки в
// SNI/QNAME/SIP/STUN (удаляет http(s)://, путь, порт и завершающую точку).
func ValidateDomain(raw string) (string, error) {
	d := strings.ToLower(strings.TrimSuffix(NormalizeDomain(raw), "."))
	if d == "" || len(d) > MaxDomainLen {
		return "", fmt.Errorf("%w: %q", ErrInvalidDomain, raw)
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("%w: %q", ErrInvalidDomain, raw)
	}
	for _, lbl := range labels {
		if len(lbl) == 0 || len(lbl) > 63 {
			return "", fmt.Errorf("%w: %q", ErrInvalidDomain, raw)
		}
		if lbl[0] == '-' || lbl[len(lbl)-1] == '-' {
			return "", fmt.Errorf("%w: %q", ErrInvalidDomain, raw)
		}
		for i := 0; i < len(lbl); i++ {
			c := lbl[i]
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
				return "", fmt.Errorf("%w: %q", ErrInvalidDomain, raw)
			}
		}
	}
	tld := labels[len(labels)-1]
	if len(tld) < 2 {
		return "", fmt.Errorf("%w: %q", ErrInvalidDomain, raw)
	}
	hasAlpha := false
	for i := 0; i < len(tld); i++ {
		if tld[i] >= 'a' && tld[i] <= 'z' {
			hasAlpha = true
			break
		}
	}
	if !hasAlpha {
		return "", fmt.Errorf("%w: %q", ErrInvalidDomain, raw)
	}
	return d, nil
}

// ParseDomainList разбирает строку доменов (через запятую, точку с запятой,
// пробел или перенос строки), валидирует каждый и убирает дубликаты.
func ParseDomainList(raw string) ([]string, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if len(fields) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		d, err := ValidateDomain(f)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
		if len(out) > MaxDomainListLen {
			return nil, fmt.Errorf("%w: too many domains (max %d)", ErrInvalidDomain, MaxDomainListLen)
		}
	}
	return out, nil
}
