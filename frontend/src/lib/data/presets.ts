export interface ServicePreset {
	id: string;
	name: string;
	subscriptions: { url: string; name: string }[];
	manualDomains?: string[];
}

// All URLs verified 2026-03-29. Format: plain domain lists (one per line).
const H = 'https://raw.githubusercontent.com/Ground-Zerro/HydraRoute/main/Neo/domain';
const I = 'https://raw.githubusercontent.com/itdoginfo/allow-domains/master/Services';
const R = 'https://raw.githubusercontent.com/RockBlack-VPN/ip-address/main/Global';

export const SERVICE_PRESETS: ServicePreset[] = [
	{
		id: 'youtube',
		name: 'YouTube',
		subscriptions: [
			{ url: `${H}/dns-youtube.txt`, name: 'HydraRoute' },
			{ url: `${I}/youtube.lst`, name: 'itdoginfo' },
		],
	},
	{
		id: 'telegram',
		name: 'Telegram',
		subscriptions: [
			{ url: `${H}/dns-telegram.txt`, name: 'HydraRoute' },
			{ url: `${I}/telegram.lst`, name: 'itdoginfo' },
		],
	},
	{
		id: 'discord',
		name: 'Discord',
		subscriptions: [
			{ url: `${H}/dns-discord.txt`, name: 'HydraRoute' },
			{ url: `${I}/discord.lst`, name: 'itdoginfo' },
		],
	},
	{
		id: 'whatsapp',
		name: 'WhatsApp',
		subscriptions: [
			{ url: `${H}/dns-whatsapp.txt`, name: 'HydraRoute' },
		],
	},
	{
		id: 'instagram',
		name: 'Instagram',
		subscriptions: [
			{ url: `${H}/dns-instagram.txt`, name: 'HydraRoute' },
		],
	},
	{
		id: 'facebook',
		name: 'Facebook',
		subscriptions: [
			{ url: `${I}/meta.lst`, name: 'itdoginfo' },
		],
	},
	{
		id: 'twitter',
		name: 'Twitter/X',
		subscriptions: [
			{ url: `${I}/twitter.lst`, name: 'itdoginfo' },
		],
	},
	{
		id: 'spotify',
		name: 'Spotify',
		subscriptions: [
			{ url: `${R}/Spotify/Spotify_domain`, name: 'RockBlack' },
		],
	},
	{
		id: 'netflix',
		name: 'Netflix',
		subscriptions: [
			{ url: `${H}/dns-netflix.txt`, name: 'HydraRoute' },
		],
	},
	{
		id: 'tiktok',
		name: 'TikTok',
		subscriptions: [
			{ url: `${H}/dns-tiktok.txt`, name: 'HydraRoute' },
			{ url: `${I}/tiktok.lst`, name: 'itdoginfo' },
		],
	},
	{
		id: 'chatgpt',
		name: 'ChatGPT',
		subscriptions: [
			{ url: `${H}/dns-openai.txt`, name: 'HydraRoute' },
		],
	},
	{
		id: 'twitch',
		name: 'Twitch',
		subscriptions: [
			{ url: `${H}/dns-twitch.txt`, name: 'HydraRoute' },
			{ url: `${R}/twitch/twitch_domain`, name: 'RockBlack' },
		],
	},
	{
		id: 'google',
		name: 'Google',
		subscriptions: [
			{ url: `${H}/dns-google.txt`, name: 'HydraRoute' },
		],
	},
	{
		id: 'github',
		name: 'GitHub',
		subscriptions: [
			{ url: `${H}/dns-github-pilot.txt`, name: 'HydraRoute' },
		],
	},
	{
		id: 'roblox',
		name: 'Roblox',
		subscriptions: [
			{ url: `${R}/Roblox/roblox_domain`, name: 'RockBlack' },
		],
	},
];
