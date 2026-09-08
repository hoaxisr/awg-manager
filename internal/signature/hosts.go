// Adapted from payloadGen (MIT) — https://github.com/Sketchystan1/payloadGen
package signature

import mrand "math/rand"

// hostPool — SNI/имена хостов для профилей. Первая часть (108 записей) —
// DOMAIN_POOL из payloadGen (порт-спека §6, порядок = вес: чем выше, тем
// чаще, дубли google.com/apple.com/microsoft.com/github.com сохранены как
// вес), затем хосты прежнего generate_hostpools.go, которых там ещё нет.
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
	"ghcr.io", "jetbrains.com", "plugins.jetbrains.com", "download.jetbrains.com", "turn.yandex.net", "stun.yandex.net",
	"stun1.yandex.net", "telemost.yandex.ru", "turn.vk.com", "stun.vk.com", "stun1.vk.com", "rtc.vk.com",
	"stun.mail.ru", "turn.mail.ru", "stun.sipnet.ru", "stun.sipnet.net", "stun.zadarma.com", "turn.zadarma.com",
	"stun.zepter.ru", "stun.mango-office.ru", "stun.beeline.ru", "stun.mts.ru", "stun.megafon.ru", "stun.rostelecom.ru",
	"stun.tele2.ru", "stun.sber.ru", "stun.stunprotocol.org", "stunserver.stunprotocol.org", "stun.voip.ipp2p.com", "stun.voipstunt.com",
	"stun.voipbuster.com", "stun.voipwise.com", "stun.voiptia.net", "stun.voxox.com", "stun.voxgratia.org", "stun.voys.nl",
	"stun.voztele.com", "stun.voipzoom.com", "stun.vopium.com", "stun.ippi.fr", "stun.antisip.com", "stun.freecall.com",
	"stun.internetcalls.com", "stun.counterpath.com", "stun.counterpath.net", "stun.softjoys.com", "stun.sipgate.net", "stun.sip.us",
	"stun.ekiga.net", "stun.ideasip.com", "stun.schlund.de", "stun.xs4all.nl", "stun.xten.com", "stun.sonetel.com",
	"stun.sonetel.net", "stun.rock.com", "stun.ooma.com", "stun.vyke.com", "stun.webcalldirect.com", "stun.wwdl.net",
	"stun.yesdates.com", "stun.yesss.at", "stun.zoiper.com", "stun01.sipphone.com", "stun1.faktortel.com.au", "stun.noc.ams-ix.net",
	"stun.xtratelecom.es", "stun.wifirst.net", "stun.whoi.edu", "stun.zadv.com", "stun.zentauron.de", "stun.voztovoice.org",
	"stun1.voiceeclipse.net", "stun.f.haeder.net", "meet.jit.si", "stun.jit.si", "turn.jit.si", "8x8.vc",
	"stun.services.mozilla.com", "turn.matrix.org", "stun.matrix.org", "stun.nextcloud.com", "turn.nextcloud.com", "janus.conf.meetecho.com",
	"stun.meetecho.com", "global.stun.twilio.com", "stun.us1.twilio.com", "stun.ie1.twilio.com", "stun.au1.twilio.com", "stun.us2.twilio.com",
	"stun.nexmo.com", "stun.vonage.com", "global.stun.bandwidth.com", "stun.plivo.com", "stun.signalwire.com", "stun.livekit.cloud",
	"stun.metered.ca", "openrelay.metered.ca", "coturn.net", "freestun.net", "relay.webwormhole.io", "expressturn.com",
	"sip.beeline.ru", "voip.beeline.ru", "sip.mts.ru", "voip.mts.ru", "sip.megafon.ru", "voip.megafon.ru",
	"sip.tele2.ru", "voip.tele2.ru", "sip.rostelecom.ru", "voip.rostelecom.ru", "sip.mtt.ru", "voip.mtt.ru",
	"sip.vk.com", "sip.yandex.ru", "sip.mail.ru", "voip.sberbank.ru", "sip.vats.sber.ru", "sip.tbank.ru",
	"sip.sipnet.ru", "sip.sipnet.net", "sip2.sipnet.ru", "sip.mango-office.ru", "pbx.mango-office.ru", "sip.zadarma.com",
	"pbx.zadarma.com", "sip.gravitel.ru", "sip.onlinepbx.ru", "sip.uis.ru", "pbx.uis.ru", "sip.comagic.ru",
	"sip.binotel.ru", "sip.novofon.ru", "sip.megacall.ru", "sip.zebra-telecom.ru", "sip.obit.ru", "sip.mtsglobaltelecom.ru",
	"pbx.rt.ru", "sip.telfin.ru", "sip.uiscom.ru", "sip.voxlink.ru", "sip.datafox.ru", "sip.sipmarket.net",
	"sip.ngs.ru", "sip.kolabora.com", "sip.sipuni.com", "sip.voximplant.com", "sip.exolve.ru", "sip.dialpad.ru",
	"sip.oblako.ru", "pbx.onlinesim.ru", "sip.onlinesim.ru", "sip.iptel.org", "sip2sip.info", "sip.linphone.org",
	"proxy.sipthor.net", "sip.sipthor.net", "sip.antisip.com", "sip.ippi.fr", "sip.voipbuster.com", "sip.voipstunt.com",
	"sip.freecall.com", "sip.powervoip.com", "sip.poivy.com", "sip.voipwise.com", "sip.internetcalls.com", "sip.counterpath.com",
	"sipml5.org", "sip.zoiper.com", "sip.microsip.org", "asterisk.org", "sip.asterisk.org", "sip.kamailio.org",
	"sip.opensips.org", "sip.freeswitch.org", "sip.vonage.com", "sip.ringcentral.com", "sip.8x8.com", "sip.plivo.com",
	"sip.telnyx.com", "sip.bandwidth.com", "sip.twilio.com", "global.sip.twilio.com", "sip.infobip.com", "sip.messagebird.com",
	"sip.signalwire.com", "sip.did.telnyx.com", "sip.livekit.cloud", "sip.dialpad.com", "sip.aircall.io", "sip.3cx.com",
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
