package traffic

import (
	"net"
	"strings"
)

type knowledgeRule struct {
	patterns    []string // domain suffixes / substrings
	title       string
	description string
	org         string
	country     string
	countryCode string
	category    string
	icon        string
}

type cidrKnowledge struct {
	net         *net.IPNet
	title       string
	description string
	org         string
	country     string
	countryCode string
	category    string
	icon        string
}

var (
	domainKnowledgeRules []knowledgeRule
	cidrKnowledgeRules   []cidrKnowledge
)

func init() {
	// 1. Domain knowledge rules
	domainKnowledgeRules = []knowledgeRule{
		// Huawei & Honor Smartphone Cloud
		{
			patterns:    []string{"dbankcloud.ru", "dbankcloud.com", "dbankcloud.cn", "hwclouds-dns.com", "hwclouds.com", "hicloud.com", "huawei.com", "honor.com", "hispace.hicloud.com", "petalmail.com", "petalpay.com", "aspirelifestyle.com"},
			title:       "Huawei & Honor / Службы устройства",
			description: "Системные службы EMUI/MagicOS: Погода, Музыка, Темы, AppGallery, аккаунт и облако Huawei",
			org:         "Huawei Technologies Co., Ltd.",
			country:     "Китай",
			countryCode: "CN",
			category:    "system",
			icon:        "smartphone",
		},
		// Google Firebase & Mobile App Telemetry
		{
			patterns:    []string{"app-measurement.com", "crashlytics.com", "firebaseio.com", "firebase.io", "googleadservices.com", "googletagmanager.com", "google-analytics.com", "doubleclick.net", "gvt1.com", "gvt2.com", "android.clients.google.com"},
			title:       "Google Firebase / Аналитика",
			description: "Телеметрия, отслеживание сбоев и аналитика мобильных приложений (Firebase SDK)",
			org:         "Google LLC",
			country:     "США",
			countryCode: "US",
			category:    "system",
			icon:        "google",
		},
		// Xiaomi / Redmi / POCO
		{
			patterns:    []string{"xiaomi.com", "mi.com", "miui.com", "xiaomi.net", "mi-img.com", "micloud.xiaomi.net", "tracking.miui.com", "api.ad.xiaomi.com", "duokan.com", "sec.miui.com"},
			title:       "Xiaomi MIUI / HyperOS",
			description: "Системные службы Xiaomi: Mi Cloud, темы, погода, обновления, синхронизация",
			org:         "Xiaomi Corporation",
			country:     "Китай",
			countryCode: "CN",
			category:    "system",
			icon:        "smartphone",
		},
		// Samsung Galaxy / OneUI
		{
			patterns:    []string{"samsung.com", "samsungcloud.com", "samsungqbe.com", "samsungapps.com", "samsungosp.com", "samsungpositioning.com", "samsungdm.com"},
			title:       "Samsung Galaxy / OneUI",
			description: "Службы Samsung: Galaxy Store, Samsung Cloud, Bixby, SmartThings, обновление ПО",
			org:         "Samsung Electronics",
			country:     "Южная Корея",
			countryCode: "KR",
			category:    "system",
			icon:        "smartphone",
		},
		// Realme / Oppo / OnePlus / Vivo (HeyTap)
		{
			patterns:    []string{"heytap.com", "heytapmobile.com", "oppo.com", "realme.com", "oneplus.com", "vivo.com", "coloros.com", "heytapcs.com"},
			title:       "Realme / Oppo / OnePlus (HeyTap)",
			description: "Системные службы и облако ColorOS / Realme UI / OxygenOS (HeyTap Cloud)",
			org:         "Guangdong HeyTap Technology",
			country:     "Китай",
			countryCode: "CN",
			category:    "system",
			icon:        "smartphone",
		},
		// Mobile Analytics & Trackers (AppsFlyer, Adjust, Sentry, etc.)
		{
			patterns:    []string{"appsflyer.com", "adjust.com", "sentry.io", "onesignal.com", "amplitude.com", "branch.io", "kochava.com", "flurry.com", "clevertap.com", "applovin.com", "unityads.unity3d.com", "adcolony.com"},
			title:       "Аналитика и трекинг приложений",
			description: "Службы сбора телеметрии, атрибуции рекламы и отслеживания ошибок (AppsFlyer/Adjust/Sentry)",
			org:         "Mobile Analytics Provider",
			country:     "США",
			countryCode: "US",
			category:    "system",
			icon:        "cloud",
		},
		// Yandex AppMetrica & Ad Network
		{
			patterns:    []string{"appmetrica.crashes.yandex.net", "appmetrica.yandex.net", "mobile.yandex.net", "tracker.yandex.net", "mc.yandex.ru", "an.yandex.ru", "adfox.ru"},
			title:       "Яндекс.Метрика / AppMetrica",
			description: "Аналитика и телеметрия мобильных приложений Яндекс AppMetrica",
			org:         "Yandex LLC",
			country:     "Россия",
			countryCode: "RU",
			category:    "system",
			icon:        "yandex",
		},
		// Google Cloud & Workspace
		{
			patterns:    []string{"googleusercontent.com", "gstatic.com", "googleapis.com", "1e100.net", "google.com", "ggpht.com"},
			title:       "Google / Облачный контент",
			description: "Файлы и контент сервисов Google (Google Диск, аватары, фото, скрипты)",
			org:         "Google LLC",
			country:     "США",
			countryCode: "US",
			category:    "cloud",
			icon:        "google",
		},
		// YouTube
		{
			patterns:    []string{"googlevideo.com", "youtube.com", "ytimg.com", "youtu.be"},
			title:       "YouTube",
			description: "Видеопотоки и превью видеороликов YouTube (Google Video CDN)",
			org:         "Google LLC",
			country:     "США",
			countryCode: "US",
			category:    "media",
			icon:        "youtube",
		},
		// Telegram
		{
			patterns:    []string{"telegram.org", "api.telegram.org", "t.me", "telesco.pe", "telegra.ph"},
			title:       "Telegram",
			description: "Мессенджер Telegram, голосовые вызовы, каналы и передача файлов",
			org:         "Telegram Messenger Inc.",
			country:     "ОАЭ",
			countryCode: "AE",
			category:    "communication",
			icon:        "telegram",
		},
		// VK / Vkontakte
		{
			patterns:    []string{"vk.com", "vk-portal.net", "userapi.com", "vkuser.net", "vkuserlive.com", "vk-cdn.net", "vk.me"},
			title:       "ВКонтакте",
			description: "Социальная сеть ВКонтакте, фотоальбомы, музыка и видеопотоки",
			org:         "VK LLC",
			country:     "Россия",
			countryCode: "RU",
			category:    "social",
			icon:        "vk",
		},
		// Mail.ru
		{
			patterns:    []string{"smailru.net", "imgsmail.ru", "mail.ru", "mrim.mail.ru", "my.mail.ru", "mradx.net", "target.my.com"},
			title:       "Mail.ru / Почта и файлы",
			description: "Почтовая служба Mail.ru, облачные диски и медиа-хранилище группы VK",
			org:         "VK LLC",
			country:     "Россия",
			countryCode: "RU",
			category:    "communication",
			icon:        "mail",
		},
		// Odnoklassniki
		{
			patterns:    []string{"ok.ru", "odnoklassniki.ru", "odkl.ru", "mycdn.me"},
			title:       "Одноклассники (VK)",
			description: "Социальная сеть Одноклассники, видео и трансляции",
			org:         "VK LLC",
			country:     "Россия",
			countryCode: "RU",
			category:    "social",
			icon:        "vk",
		},
		// RuStore
		{
			patterns:    []string{"rustore.ru", "help.rustore.ru", "backapi.rustore.ru"},
			title:       "RuStore",
			description: "Официальный российский магазин мобильных приложений RuStore",
			org:         "VK LLC",
			country:     "Россия",
			countryCode: "RU",
			category:    "system",
			icon:        "vk",
		},
		// Yandex Kinopoisk
		{
			patterns:    []string{"kinopoisk.ru", "ott.yandex.net", "hd.kinopoisk.ru"},
			title:       "Кинопоиск",
			description: "Онлайн-кинотеатр Кинопоиск, потоковое видео и сериалы",
			org:         "Yandex LLC",
			country:     "Россия",
			countryCode: "RU",
			category:    "media",
			icon:        "yandex",
		},
		// Yandex Core Services
		{
			patterns:    []string{"yandex.ru", "yandex.net", "ya.ru", "yastatic.net", "yandexcloud.net", "dzen.ru"},
			title:       "Яндекс / Сервисы",
			description: "Поисковые, медиа, картографические и облачные сервисы Яндекса",
			org:         "Yandex LLC",
			country:     "Россия",
			countryCode: "RU",
			category:    "cloud",
			icon:        "yandex",
		},
		// Banking: T-Bank (Tinkoff)
		{
			patterns:    []string{"tinkoff.ru", "tbank.ru", "tcsbank.ru"},
			title:       "Т-Банк (Тинькофф)",
			description: "Банковские сервисы, мобильное приложение и экосистема Т-Банка",
			org:         "АО ТБанк",
			country:     "Россия",
			countryCode: "RU",
			category:    "cloud",
			icon:        "server",
		},
		// Banking: Sber
		{
			patterns:    []string{"sberbank.ru", "sberbank.net", "sber.ru", "sberdevices.ru"},
			title:       "СберБанк",
			description: "Банковские сервисы, платежные шлюзы и экосистема Сбера",
			org:         "ПАО Сбербанк",
			country:     "Россия",
			countryCode: "RU",
			category:    "cloud",
			icon:        "server",
		},
		// Banking: Alfa-Bank
		{
			patterns:    []string{"alfabank.ru", "alfabank.net"},
			title:       "Альфа-Банк",
			description: "Банковские сервисы и мобильное приложение Альфа-Банк",
			org:         "АО Альфа-Банк",
			country:     "Россия",
			countryCode: "RU",
			category:    "cloud",
			icon:        "server",
		},
		// E-Commerce: Wildberries
		{
			patterns:    []string{"wildberries.ru", "wb.ru", "wbstatic.net"},
			title:       "Wildberries",
			description: "Маркетплейс Wildberries, карточки товаров и медиа-контент",
			org:         "ООО Вайлдберриз",
			country:     "Россия",
			countryCode: "RU",
			category:    "media",
			icon:        "globe",
		},
		// E-Commerce: Ozon
		{
			patterns:    []string{"ozon.ru", "ozon-st.cdn.ngenix.net", "ozoncontent.ru"},
			title:       "Ozon",
			description: "Маркетплейс Ozon и сеть доставки контента",
			org:         "ООО Интернет Решения (Ozon)",
			country:     "Россия",
			countryCode: "RU",
			category:    "media",
			icon:        "globe",
		},
		// Classifieds: Avito
		{
			patterns:    []string{"avito.ru", "avito.st"},
			title:       "Авито",
			description: "Сервис объявлений и мобильное приложение Авито",
			org:         "ООО Кех еКоммерц (Avito)",
			country:     "Россия",
			countryCode: "RU",
			category:    "social",
			icon:        "globe",
		},
		// TikTok
		{
			patterns:    []string{"tiktok.com", "byteoversea.com", "ibytedtos.com", "tiktokcdn.com", "ttwstatic.com"},
			title:       "TikTok",
			description: "Социальная сеть TikTok и сеть доставки видео ByteDance",
			org:         "ByteDance Ltd.",
			country:     "Сингапур",
			countryCode: "SG",
			category:    "media",
			icon:        "media",
		},
		// Discord
		{
			patterns:    []string{"discord.gg", "discord.com", "discordapp.com", "discordapp.net", "discord.media"},
			title:       "Discord",
			description: "Голосовой и текстовый мессенджер Discord",
			org:         "Discord Inc.",
			country:     "США",
			countryCode: "US",
			category:    "communication",
			icon:        "chat",
		},
		// Spotify
		{
			patterns:    []string{"spotify.com", "scdn.co", "spotifycdn.com", "audio-ak-spotify-com.akamaized.net", "spotify.map.fastly.net", "spotify"},
			title:       "Spotify",
			description: "Музыкальный стриминговый сервис Spotify",
			org:         "Spotify AB",
			country:     "Швеция",
			countryCode: "SE",
			category:    "media",
			icon:        "media",
		},
		// Netflix
		{
			patterns:    []string{"netflix.com", "nflxvideo.net", "nflximg.net", "nflxext.com"},
			title:       "Netflix",
			description: "Стриминговый сервис фильмов и сериалов Netflix",
			org:         "Netflix, Inc.",
			country:     "США",
			countryCode: "US",
			category:    "media",
			icon:        "media",
		},
		// Akamai CDN
		{
			patterns:    []string{"akamaitechnologies.com", "akamai.net", "akamaized.net", "edgesuite.net"},
			title:       "Akamai CDN",
			description: "Международная сеть доставки контента для веб-сайтов и приложений",
			org:         "Akamai Technologies",
			country:     "США",
			countryCode: "US",
			category:    "cloud",
			icon:        "cloud",
		},
		// Cloudflare CDN
		{
			patterns:    []string{"cloudflare.com", "cloudflare.net", "cloudflare-dns.com"},
			title:       "Cloudflare CDN",
			description: "Глобальная облачная сеть защиты, кэширования и DNS",
			org:         "Cloudflare, Inc.",
			country:     "США",
			countryCode: "US",
			category:    "cloud",
			icon:        "cloud",
		},
		// GitHub
		{
			patterns:    []string{"github.com", "githubusercontent.com", "github.io", "githubassets.com"},
			title:       "GitHub",
			description: "Платформа разработки, репозитории исходного кода и пакеты",
			org:         "GitHub, Inc. (Microsoft)",
			country:     "США",
			countryCode: "US",
			category:    "dev",
			icon:        "github",
		},
		// Meta / WhatsApp / Instagram
		{
			patterns:    []string{"fbcdn.net", "whatsapp.net", "whatsapp.com", "instagram.com", "facebook.com"},
			title:       "Meta / WhatsApp / Instagram",
			description: "Инфраструктура мессенджера WhatsApp и медиа-серверов Meta",
			org:         "Meta Platforms, Inc.",
			country:     "США",
			countryCode: "US",
			category:    "communication",
			icon:        "chat",
		},
		// Telecom: Beltelecom / A1
		{
			patterns:    []string{"telecom.by", "beltelecom.by", "a1.by"},
			title:       "Белтелеком / A1 Беларусь",
			description: "Белорусский национальный оператор связи и интернет-магистраль",
			org:         "РУП Белтелеком / A1",
			country:     "Беларусь",
			countryCode: "BY",
			category:    "isp",
			icon:        "server",
		},
		// Telecom: MTS Russia
		{
			patterns:    []string{"mts.ru", "mts-nn.ru", "spb.mts.ru"},
			title:       "МТС (Россия)",
			description: "Инфраструктура и технологические шлюзы оператора связи МТС",
			org:         "ПАО МТС",
			country:     "Россия",
			countryCode: "RU",
			category:    "isp",
			icon:        "server",
		},
		// Telecom: Beeline
		{
			patterns:    []string{"omni.ru", "corbina.ru", "beeline.ru"},
			title:       "Билайн / Corbina Telecom",
			description: "Инфраструктура и шлюзы провайдера Билайн (ВымпелКом)",
			org:         "ПАО ВымпелКом",
			country:     "Россия",
			countryCode: "RU",
			category:    "isp",
			icon:        "server",
		},
		// Telecom: Dom.ru
		{
			patterns:    []string{"ertelecom.ru", "domru.ru"},
			title:       "Дом.ру / ЭР-Телеком",
			description: "Интернет-провайдер Дом.ру (ЭР-Телеком Холдинг)",
			org:         "АО ЭР-Телеком Холдинг",
			country:     "Россия",
			countryCode: "RU",
			category:    "isp",
			icon:        "server",
		},
		// Steam (Valve)
		{
			patterns:    []string{"steamcommunity.com", "steampowered.com", "steamstatic.com", "valvesoftware.com"},
			title:       "Steam (Valve)",
			description: "Игровая платформа Steam, загрузка обновлений игр и игровое сообщество",
			org:         "Valve Corporation",
			country:     "США",
			countryCode: "US",
			category:    "gaming",
			icon:        "game",
		},
		// PlayStation
		{
			patterns:    []string{"playstation.net", "playstation.com", "sonyentertainmentnetwork.com"},
			title:       "PlayStation Network",
			description: "Игровая сеть Sony PlayStation, сетевые игры и цифровой магазин PSN",
			org:         "Sony Interactive Entertainment",
			country:     "Япония",
			countryCode: "JP",
			category:    "gaming",
			icon:        "game",
		},
		// Microsoft / Windows / Xbox
		{
			patterns:    []string{"xboxlive.com", "microsoft.com", "windowsupdate.com", "azure.com", "live.com", "msftconnecttest.com"},
			title:       "Microsoft / Windows / Xbox",
			description: "Обновления Windows, облако Azure и сетевые службы Microsoft",
			org:         "Microsoft Corporation",
			country:     "США",
			countryCode: "US",
			category:    "system",
			icon:        "windows",
		},
		// Apple / iOS
		{
			patterns:    []string{"apple.com", "icloud.com", "mzstatic.com", "aaplimg.com", "apple-dns.net"},
			title:       "Apple / iCloud",
			description: "Службы Apple: синхронизация iCloud, App Store, пуш-уведомления",
			org:         "Apple Inc.",
			country:     "США",
			countryCode: "US",
			category:    "cloud",
			icon:        "apple",
		},
		// Vivaldi Browser
		{
			patterns:    []string{"vivaldi.com", "vivaldi.net"},
			title:       "Vivaldi Browser",
			description: "Службы синхронизации закладок, настроек и обновлений браузера Vivaldi",
			org:         "Vivaldi Technologies",
			country:     "Норвегия",
			countryCode: "NO",
			category:    "software",
			icon:        "globe",
		},
	}

	// 2. Known CIDR knowledge rules
	rawCIDRs := []struct {
		cidr, title, desc, org, country, cc, cat, icon string
	}{
		{"5.101.40.0/22", "Одноклассники (VK)", "Серверы социальной сети Одноклассники и медиа-платформы VK", "VK LLC", "Россия", "RU", "social", "vk"},
		{"94.139.240.0/20", "Яндекс.Облако", "Облачная инфраструктура и серверы Yandex.Cloud", "Yandex.Cloud LLC", "Россия", "RU", "cloud", "yandex"},
		{"46.53.0.0/16", "A1 / Белтелеком", "Белорусский интернет-провайдер и магистральная сеть", "Unitary enterprise A1", "Беларусь", "BY", "isp", "server"},
		{"185.162.92.0/22", "Дата-центр Миран", "Российский хостинг и дата-центр Миран (Санкт-Петербург)", "Miran Data Center", "Россия", "RU", "cloud", "server"},
		{"95.163.0.0/16", "Mail.ru / VK", "Почтовые серверы, медиа-хранилище и авторизация Mail.ru", "VK LLC", "Россия", "RU", "communication", "mail"},
		{"149.154.160.0/20", "Telegram", "Серверы дата-центров мессенджера Telegram", "Telegram Messenger Inc.", "Нидерланды", "NL", "communication", "telegram"},
		{"91.108.4.0/22", "Telegram", "Серверы дата-центров мессенджера Telegram", "Telegram Messenger Inc.", "Нидерланды", "NL", "communication", "telegram"},
		{"91.108.56.0/22", "Telegram", "Серверы веб-версии и звонков Telegram", "Telegram Messenger Inc.", "Нидерланды", "NL", "communication", "telegram"},
		{"172.217.0.0/16", "YouTube / Google", "Серверы кэширования и видеопотоков YouTube (Google Video)", "Google LLC", "США", "US", "media", "youtube"},
		{"142.250.0.0/15", "Google / YouTube", "Глобальная инфраструктура Google и YouTube", "Google LLC", "США", "US", "media", "google"},
		{"209.85.128.0/17", "YouTube Video CDN", "Серверы кэширования и видеопотоков YouTube", "Google LLC", "США", "US", "media", "youtube"},
		{"216.58.192.0/19", "Google Search / API", "Серверы поисковой системы и API Google", "Google LLC", "США", "US", "cloud", "google"},
		{"140.82.112.0/20", "GitHub", "Инфраструктура хостинга репозиториев GitHub", "GitHub, Inc.", "США", "US", "dev", "github"},
		{"185.199.108.0/22", "GitHub Pages / CDN", "Сеть доставки статического контента GitHub", "GitHub, Inc.", "США", "US", "dev", "github"},
		{"77.88.0.0/18", "Яндекс", "Дата-центры и серверы экосистемы Яндекс", "Yandex LLC", "Россия", "RU", "cloud", "yandex"},
		{"87.250.224.0/19", "Яндекс / Кинопоиск", "Медиа-серверы и онлайн-кинотеатр Кинопоиск", "Yandex LLC", "Россия", "RU", "media", "yandex"},
		{"95.213.0.0/16", "ВКонтакте", "Серверы и кэш дата-центров социальной сети ВКонтакте", "VK LLC", "Россия", "RU", "social", "vk"},
		{"87.240.128.0/18", "ВКонтакте", "Медиа-серверы и стриминг ВКонтакте", "VK LLC", "Россия", "RU", "social", "vk"},
		{"155.133.224.0/19", "Steam (Valve)", "Серверы авторизации и загрузки игр Steam", "Valve Corporation", "США", "US", "gaming", "game"},
		{"162.254.192.0/19", "Steam (Valve)", "Игровые серверы Valve и релеи матчей CS/Dota", "Valve Corporation", "США", "US", "gaming", "game"},
		{"104.16.0.0/12", "Cloudflare CDN", "Глобальная распределенная сеть Cloudflare", "Cloudflare, Inc.", "США", "US", "cloud", "cloud"},
		{"172.64.0.0/13", "Cloudflare CDN", "Сеть кэширования и защиты Cloudflare", "Cloudflare, Inc.", "США", "US", "cloud", "cloud"},
		{"162.158.0.0/15", "Cloudflare Proxy", "Облачная сеть Cloudflare Proxy & CDN", "Cloudflare, Inc.", "США", "US", "cloud", "cloud"},
		{"17.0.0.0/8", "Apple Services", "Инфраструктура Apple iCloud, Push и App Store", "Apple Inc.", "США", "US", "cloud", "apple"},
		{"20.0.0.0/8", "Microsoft / Azure", "Облачная платформа Microsoft Azure и Windows Update", "Microsoft Corporation", "США", "US", "cloud", "windows"},
		{"40.64.0.0/10", "Microsoft / Azure", "Сервисы Microsoft Office 365, Teams и Azure", "Microsoft Corporation", "США", "US", "cloud", "windows"},
		{"31.13.64.0/18", "Meta / WhatsApp", "Серверы обмена сообщениями и звонков WhatsApp", "Meta Platforms, Inc.", "США", "US", "communication", "chat"},
		{"157.240.0.0/16", "Meta / Instagram", "Серверы фото и видеоленты Instagram", "Meta Platforms, Inc.", "США", "US", "social", "chat"},
		{"57.144.0.0/16", "Meta Platforms", "Инфраструктура серверов Meta Platforms", "Meta Platforms, Inc.", "США", "US", "social", "chat"},
		{"173.194.0.0/16", "YouTube / Google", "Серверы кэширования и видеопотоков YouTube (Google Video)", "Google LLC", "США", "US", "media", "youtube"},
		{"108.177.0.0/17", "Google Video", "Серверы доставки видеоконтента Google", "Google LLC", "США", "US", "media", "youtube"},
		{"185.76.0.0/16", "Fastly / Discord", "Сеть доставки медиа и контента Discord", "Fastly, Inc.", "США", "US", "communication", "chat"},
		{"194.221.250.0/24", "Akamai CDN", "Распределенная сеть доставки контента Akamai", "Akamai Technologies", "Европа", "EU", "cloud", "cloud"},
		{"96.211.229.0/24", "Akamai CDN", "Распределенная сеть доставки контента Akamai", "Akamai Technologies", "США", "US", "cloud", "cloud"},
		{"15.197.0.0/16", "AWS CloudFront", "Глобальная сеть доставки контента Amazon CloudFront", "Amazon.com, Inc.", "США", "US", "cloud", "cloud"},
		{"169.197.114.0/23", "CDN77 / Datacamp", "Сеть доставки видео и контента CDN77", "DataCamp Limited", "Великобритания", "GB", "cloud", "cloud"},
		{"195.181.160.0/19", "CDN77 / DataCamp", "Сеть доставки медиа и видеоконтента CDN77 / DataCamp", "DataCamp Limited", "Великобритания", "GB", "media", "cloud"},
		{"185.107.56.0/22", "CDN77 / DataCamp", "Сеть доставки медиа и видеоконтента CDN77", "DataCamp Limited", "Великобритания", "GB", "media", "cloud"},
		{"145.239.0.0/16", "OVHcloud", "Облачная инфраструктура и серверы OVH", "OVH SAS", "Франция", "FR", "cloud", "server"},
		{"31.3.152.0/21", "M247 Cloud", "Дата-центр и транзитная сеть M247", "M247 Ltd", "Великобритания", "GB", "cloud", "server"},
		{"211.239.0.0/16", "KT Media CDN", "Сеть доставки медиа и кэширования Korea Telecom", "Korea Telecom", "Южная Корея", "KR", "media", "globe"},
		{"212.143.0.0/16", "Bezeq International", "Магистральная сеть и CDN Bezeq", "Bezeq Int.", "Израиль", "IL", "isp", "globe"},
		{"88.198.0.0/16", "Hetzner Cloud", "Дата-центр и облачные серверы Hetzner", "Hetzner Online GmbH", "Германия", "DE", "cloud", "server"},
		{"95.216.0.0/15", "Hetzner Cloud", "Дата-центр Hetzner Хельсинки", "Hetzner Online GmbH", "Финляндия", "FI", "cloud", "server"},
		{"159.69.0.0/16", "Hetzner Cloud", "Дата-центр Hetzner Нюрнберг", "Hetzner Online GmbH", "Германия", "DE", "cloud", "server"},
		{"65.108.0.0/15", "Hetzner Cloud", "Дата-центр Hetzner Хельсинки", "Hetzner Online GmbH", "Финляндия", "FI", "cloud", "server"},
	}

	for _, item := range rawCIDRs {
		if _, ipnet, err := net.ParseCIDR(item.cidr); err == nil {
			cidrKnowledgeRules = append(cidrKnowledgeRules, cidrKnowledge{
				net:         ipnet,
				title:       item.title,
				description: item.desc,
				org:         item.org,
				country:     item.country,
				countryCode: item.cc,
				category:    item.cat,
				icon:        item.icon,
			})
		}
	}
}

// FindDomainKnowledge finds user-friendly information about a domain or IP.
func FindDomainKnowledge(domain, ip string) *DomainKnowledge {
	target := strings.ToLower(domain)

	// 1. Match domain rules
	if target != "" {
		for _, rule := range domainKnowledgeRules {
			for _, pattern := range rule.patterns {
				if target == pattern || strings.HasSuffix(target, "."+pattern) || strings.Contains(target, pattern) {
					return &DomainKnowledge{
						Title:       rule.title,
						Description: rule.description,
						Org:         rule.org,
						Country:     rule.country,
						CountryCode: rule.countryCode,
						Category:    rule.category,
						Icon:        rule.icon,
					}
				}
			}
		}
	}

	// 2. Match CIDR rules by IP
	if ip != "" {
		if parsedIP := net.ParseIP(ip); parsedIP != nil {
			for _, rule := range cidrKnowledgeRules {
				if rule.net.Contains(parsedIP) {
					return &DomainKnowledge{
						Title:       rule.title,
						Description: rule.description,
						Org:         rule.org,
						Country:     rule.country,
						CountryCode: rule.countryCode,
						Category:    rule.category,
						Icon:        rule.icon,
					}
				}
			}
		}
	}

	// 3. Fallback: If it's a known domain pattern (e.g. .ru, .by, .com)
	if target != "" && !isIPString(target) {
		title := cleanTitleFromDomain(target)
		return &DomainKnowledge{
			Title:       title,
			Description: "Веб-сервер / Доменное имя " + target,
			Category:    "general",
			Icon:        "globe",
		}
	}

	return nil
}

func isIPString(s string) bool {
	return net.ParseIP(s) != nil
}

func cleanTitleFromDomain(d string) string {
	parts := strings.Split(d, ".")
	if len(parts) >= 2 {
		base := parts[len(parts)-2]
		if len(base) > 1 {
			return strings.ToUpper(base[:1]) + base[1:]
		}
	}
	return d
}
