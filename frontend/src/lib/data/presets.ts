export interface ServicePreset {
	id: string;
	name: string;
	domains?: string[];
	subscriptionUrl?: string;
	covers?: string[];  // IDs of other presets covered by this one
}

// Domain lists verified and cleaned 2026-03-29.
// Sources: HydraRoute, itdoginfo/allow-domains, RockBlack-VPN/ip-address.
// Merged, deduplicated, BOM-stripped, problematic entries fixed.

export const SERVICE_PRESETS: ServicePreset[] = [
	{
		id: 'youtube',
		name: 'YouTube',
		domains: [
			'ggpht.com', 'googlevideo.com', 'jnn-pa.googleapis.com', 'nhacmp3youtube.com',
			'returnyoutubedislikeapi.com', 'wide-youtube.l.google.com', 'youtu.be', 'youtube.com',
			'youtubeembeddedplayer.googleapis.com', 'youtubei.googleapis.com', 'youtubekids.com',
			'youtube-nocookie.com', 'youtube-ui.l.google.com', 'yt3.googleusercontent.com',
			'yt.be', 'ytimg.com', 'ytimg.l.google.com', 'yting.com', 'yt-video-upload.l.google.com',
		],
	},
	{
		id: 'telegram',
		name: 'Telegram',
		domains: [
			'149.154.160.0/20', '185.76.151.0/24', '2001:67c:4e8::/48', '2001:b28:f23c::/48',
			'2001:b28:f23d::/48', '2001:b28:f23f::/48', '2a0a:f280::/32', '91.105.192.0/23',
			'91.108.12.0/22', '91.108.16.0/22', '91.108.20.0/22', '91.108.4.0/22',
			'91.108.56.0/22', '91.108.8.0/22', 'cdn-telegram.org', 'comments.app', 'contest.com',
			'fragment.com', 'graph.org', 'quiz.directory', 'tdesktop.com', 'telega.one',
			'telegram-cdn.org', 'telegram.dog', 'telegram.me', 'telegram.org', 'telegram.space',
			'telegra.ph', 'telesco.pe', 'tg.dev', 't.me', 'ton.org', 'tx.me', 'usercontent.dev',
		],
	},
	{
		id: 'discord',
		name: 'Discord',
		domains: [
			'dis.gd', 'disboard.org', 'discord-activities.com', 'discordactivities.com',
			'discord.app', 'discordapp.com', 'discordapp.io', 'discordapp.net',
			'discord-attachments-uploads-prd.storage.googleapis.com', 'discordbee.com',
			'discordbotlist.com', 'discordcdn.com', 'discord.center', 'discord.co', 'discord.com',
			'discord.design', 'discord.dev', 'discordexpert.com', 'discord.gg', 'discord.gift',
			'discord.gifts', 'discordhome.com', 'discordhub.com', 'discordinvites.net',
			'discordlist.me', 'discordlist.space', 'discord.me', 'discord.media',
			'discordmerch.com', 'discord.new', 'discordpartygames.com', 'discordsays.com',
			'discords.com', 'discordservers.com', 'discord.st', 'discordstatus.com',
			'discord.store', 'discord.tools', 'discordtop.com', 'disforge.com', 'dyno.gg',
			'findadiscord.com', 'mee6.xyz', 'top.gg',
		],
	},
	{
		id: 'social',
		name: 'Instagram, Facebook, WhatsApp',
		domains: [
			'bookstagram.com', 'carstagram.com', 'cdninstagram.com', 'chickstagram.com',
			'circlecrewpinkcrowd.com', 'facebook.com', 'facebook.net', 'fb.com', 'fbcdn.net',
			'fbsbx.com', 'ig.me', 'imstagram.com', 'imtagram.com', 'instaadder.com',
			'instachecker.com', 'instafallow.com', 'instafollower.com', 'instagainer.com',
			'instagda.com', 'instagify.com', 'instagmania.com', 'instagor.com',
			'instagram-brand.com', 'instagram.com', 'instagram-engineering.com',
			'instagramhashtags.net', 'instagram-help.com', 'instagramhilecim.com',
			'instagramhilesi.org', 'instagramium.com', 'instagramizlenme.com',
			'instagramkusu.com', 'instagramlogin.com', 'instagrampartners.com',
			'instagramphoto.com', 'instagram-press.com', 'instagram-press.net', 'instagramq.com',
			'instagramsepeti.com', 'instagramtips.com', 'instagramtr.com', 'instagy.com',
			'instamgram.com', 'instanttelegram.com', 'instaplayer.net', 'instastyle.tv',
			'instgram.com', 'internalfb.com', 'meta.com', 'oculus.com', 'oninstagram.com',
			'online-instagram.com', 'onlineinstagram.com', 'threads.net', 'wa.me',
			'web-instagram.net', 'whatsapp.biz', 'whatsappbrand.com', 'whatsapp.cc',
			'whatsapp.com', 'whatsapp.info', 'whatsapp.net', 'whatsapp.org',
			'whatsapp-plus.info', 'whatsapp-plus.me', 'whatsapp-plus.net', 'whatsapp.tv',
			'wl.co', 'wwwinstagram.com',
		],
	},
	{
		id: 'twitter',
		name: 'Twitter/X',
		domains: [
			'ads-twitter.com', 'cms-twdigitalassets.com', 'periscope.tv', 'pscp.tv', 't.co',
			'tellapart.com', 'tweetdeck.com', 'twimg.com', 'twitpic.com', 'twitter.biz',
			'twitter.com', 'twittercommunity.com', 'twitterflightschool.com', 'twitterinc.com',
			'twitter.jp', 'twitteroauth.com', 'twitterstat.us', 'twtrdns.net', 'twttr.com',
			'twttr.net', 'twvid.com', 'vine.co', 'x.com',
		],
	},
	{
		id: 'spotify',
		name: 'Spotify',
		domains: [
			'audio4-ak-spotify-com.akamaized.net', 'audio-ak-spotify-com.akamaized.net',
			'cdn-spotify-experiments.conductrics.com', 'heads4-ak-spotify-com.akamaized.net',
			'heads-ak-spotify-com.akamaized.net', 'pscdn.co', 'scdn.co', 'spoti.fi',
			'spotifycdn.com', 'spotifycdn.net', 'spotifycharts.com', 'spotifycodes.com',
			'spotify.com', 'spotify.com.edgesuite.net', 'spotify.design',
			'spotify-everywhere.com', 'spotifyforbrands.com', 'spotifyjobs.com',
			'spotify.map.fastlylb.net', 'spotify.map.fastly.net',
		],
	},
	{
		id: 'netflix',
		name: 'Netflix',
		domains: [
			'fast.com', 'netflix.ca', 'netflix.com', 'netflixdnstest1.com', 'netflixdnstest2.com',
			'netflixdnstest3.com', 'netflixdnstest4.com', 'netflixdnstest5.com',
			'netflixdnstest6.com', 'netflixdnstest7.com', 'netflixdnstest8.com',
			'netflixdnstest9.com', 'netflixdnstest10.com', 'netflixinvestor.com', 'netflix.net',
			'netflixstudios.com', 'netflixtechblog.com', 'nflxext.com', 'nflximg.com',
			'nflxsearch.net', 'nflxso.net', 'nflxvideo.net',
		],
	},
	{
		id: 'tiktok',
		name: 'TikTok',
		domains: [
			'account-tiktok.com', 'byteoversea.com', 'ibytedtos.com', 'ibyteimg.com',
			'muscdn.com', 'musical.ly', 'tik-tokapi.com', 'tiktokcdn.com', 'tiktokcdn-eu.com',
			'tiktokcdn-us.com', 'tiktok.com', 'tiktokd.net', 'tiktokd.org',
			'tiktokglobalshop.com', 'tiktokv.com', 'tiktokv.eu', 'tiktokv.us', 'tiktokw.eu',
			'tiktokw.us', 'ttwstatic.com',
		],
	},
	{
		id: 'chatgpt',
		name: 'ChatGPT',
		domains: [
			'chatgpt.com', 'gpt3-openai.com', 'oaistatic.com', 'oaiusercontent.com',
			'openai.com', 'openai.fund', 'openai.org',
		],
	},
	{
		id: 'twitch',
		name: 'Twitch',
		domains: [
			'ext-twitch.tv', 'jtvnw.net', 'live-video.net', 'ttvnw.net', 'twitch.a2z.com',
			'twitchcdn.net', 'twitchcdn-shadow.net', 'twitch-shadow.net', 'twitchsvc.net',
			'twitchsvc-staging.tech', 'twitch.tv',
		],
	},
	{
		id: 'google',
		name: 'Google',
		domains: [
			'android.com', 'appspot.com', 'ggpht.com', 'google-analytics.com', 'googleapis.com',
			'google.com', 'googleusercontent.com', 'googlezip.net', 'gstatic.com', 'gvt1.com',
			'gvt2.com', 'gvt3.com', 'pki.goog',
		],
	},
	{
		id: 'github',
		name: 'GitHub',
		domains: [
			'applicationinsights.io', 'exp-tas.com', 'github.com', 'githubcopilot.com',
			'githubusercontent.com',
		],
	},
	{
		id: 'roblox',
		name: 'Roblox',
		domains: [
			'a1387.d.akamai.net', 'a346.dscd.akamai.net', 'accountinformation.roblox.com',
			'accountsettings.roblox.com', 'apis.rbxcdn.com', 'apis.roblox.com',
			'arkoselabs.roblox.com', 'assetdelivery.roblox.com', 'assetgame.roblox.com',
			'auth.roblox.com', 'avatar.roblox.com', 'catalog.roblox.com',
			'clientsettings.roblox.com', 'clientsettingscdn.roblox.com', 'contacts.roblox.com',
			'css.rbxcdn.com', 'd19ha9ylcjiuiu.cloudfront.net', 'd3smszjb1gn4q5.cloudfront.net',
			'e7229.f.akamaiedge.net', 'economy.roblox.com', 'ecsv2.roblox.com',
			'edge-term4-fra4.roblox.com', 'ephemeralcounters.api.roblox.com',
			'files.withpersona.com', 'firebaselogging.googleapis.com', 'friends.roblox.com',
			'fts.rbxcdn.com', 'gameinternationalization.roblox.com', 'gamejoin.roblox.com',
			'games.roblox.com', 'groups.roblox.com', 'images.rbxcdn.com', 'js.rbxcdn.com',
			'locale.roblox.com', 'metrics.roblox.com', 'notifications.roblox.com',
			'otpi0g-inapps.appsflyersdk.com', 'otpi0g-launches.appsflyersdk.com',
			'presence.roblox.com', 'privatemessages.roblox.com', 'rbxcdn.com',
			'realtime-signalr.roblox.com', 'roblox.com', 'thumbnails.roblox.com',
			'thumbnailsdelivery.roblox.com', 'tracing.roblox.com', 'tr.rbxcdn.com',
			'usermoderation.roblox.com', 'users.roblox.com', 'voice.roblox.com',
			'withpersona.com', 'www.roblox.com',
		],
	},
	{
		id: 'all-blocked',
		name: 'ItDog Allow Domains',
		subscriptionUrl: 'https://raw.githubusercontent.com/itdoginfo/allow-domains/master/Russia/inside-raw.lst',
		covers: ['youtube', 'discord', 'social', 'twitter', 'spotify', 'netflix', 'tiktok', 'chatgpt', 'google'],
	},
];
