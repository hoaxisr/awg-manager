export interface ServiceIconConfig {
	svg?: string;
	background: string;
	viewBox?: string;
	scale?: number;
	/** Served from frontend/static (e.g. /brand-icons/yandex.png). */
	assetSrc?: string;
	assetFit?: 'contain' | 'cover';
	/** CSS filter when the asset is a dark logo on a colored tile. */
	assetFilter?: string;
}

const ICONS: { keywords: string[]; config: ServiceIconConfig }[] = [
	{
		keywords: ['dev tools', 'devops', 'developer'],
		config: {
			svg: '<rect x="2.5" y="3.5" width="19" height="17" rx="2.5" fill="none" stroke="white" stroke-width="1.8"/><line x1="2.5" y1="7.5" x2="21.5" y2="7.5" stroke="white" stroke-width="1.4" opacity="0.5"/><path d="M7 10l3 3-3 3" fill="none" stroke="white" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/><line x1="12.5" y1="16" x2="17" y2="16" stroke="white" stroke-width="1.8" stroke-linecap="round"/>',
			background: '#0f172a',
		},
	},
	{
		keywords: ['disney', 'disney+'],
		config: {
			assetSrc: '/brand-icons/disney.png',
			background: '#113CCF',
			assetFilter: 'brightness(0) invert(1)',
			scale: 0.82,
		},
	},
	{
		keywords: ['yandex', 'яндекс'],
		config: {
			svg: '<path d="M2.04 12c0-5.523 4.476-10 10-10 5.522 0 10 4.477 10 10s-4.478 10-10 10c-5.524 0-10-4.477-10-10z" fill="#FC3F1D"/><path d="M13.32 7.666h-.924c-1.694 0-2.585.858-2.585 2.123 0 1.43.616 2.1 1.881 2.959l1.045.704-3.003 4.487H7.49l2.695-4.014c-1.55-1.111-2.42-2.19-2.42-4.015 0-2.288 1.595-3.85 4.62-3.85h3.003v11.868H13.32V7.666z" fill="#fff"/>',
			background: '#FC3F1D',
			scale: 1,
		},
	},
	{
		keywords: ['linkedin'],
		config: {
			svg: '<path fill="white" d="M20.447 20.452h-3.554v-5.569c0-1.328-.027-3.037-1.852-3.037-1.853 0-2.136 1.445-2.136 2.939v5.667H9.351V9h3.414v1.561h.046c.477-.9 1.637-1.85 3.37-1.85 3.601 0 4.267 2.37 4.267 5.455v6.286zM5.337 7.433c-1.144 0-2.063-.926-2.063-2.065 0-1.138.92-2.063 2.063-2.063 1.14 0 2.064.925 2.064 2.063 0 1.139-.925 2.065-2.064 2.065zm1.782 13.019H3.555V9h3.564v11.452zM22.225 0H1.771C.792 0 0 .774 0 1.729v20.542C0 23.227.792 24 1.771 24h20.451C23.2 24 24 23.227 24 22.271V1.729C24 .774 23.2 0 22.222 0h.003z"/>',
			background: '#0a66c2',
		},
	},
	{
		keywords: ['amazon'],
		config: {
			svg: '<path fill="white" d="M.045 18.02c.072-.116.187-.124.348-.022 3.636 2.11 7.594 3.166 11.87 3.166 2.852 0 5.668-.533 8.447-1.595l.315-.14c.2-.09.368-.12.504-.09.136.03.2.127.2.296 0 .22-.168.453-.504.696-1.527 1.1-3.406 1.98-5.634 2.637-2.228.656-4.36.984-6.396.984-2.148 0-4.192-.38-6.132-1.14C1.124 22.053.045 20.988.045 20.012v-1.992z"/><path fill="white" d="M6.578 13.02c0-1.092.26-2.03.78-2.816.52-.786 1.22-1.38 2.1-1.784.96-.44 2.1-.76 3.42-.96.48-.064 1.26-.14 2.34-.224v-.448c0-.876-.1-1.48-.3-1.812-.34-.536-.9-.804-1.68-.804h-.18c-.56.04-1.03.196-1.4.468-.376.272-.636.696-.78 1.272-.06.252-.168.4-.324.44l-2.04-.224c-.18-.04-.28-.14-.28-.296 0-.04.01-.09.04-.156.36-1.86 1.54-2.896 3.54-3.108l.54-.048c1.54 0 2.76.352 3.66 1.056.14.12.27.252.38.396.12.144.21.28.28.408.08.128.15.3.22.516.07.216.12.392.14.528.03.136.05.34.07.612.02.272.03.472.03.6v5.4c0 .38.06.7.18.96.12.26.22.44.32.54.1.1.26.24.48.42.1.08.15.18.15.3 0 .08-.04.16-.12.24-.56.496-1.22 1.076-1.22 1.076l-.14.08c-.16.08-.34.08-.52-.04-.36-.32-.6-.54-.72-.66l-.24-.264c-.72.756-1.54 1.18-2.46 1.272l-.6.048c-.96 0-1.74-.288-2.34-.864-.6-.576-.9-1.348-.9-2.316zm3.38-.816c0 .544.16.984.48 1.32.32.336.72.504 1.2.504.04 0 .1-.004.18-.012s.14-.016.18-.024c.64-.176 1.14-.58 1.5-1.212.2-.36.34-.748.42-1.164.08-.416.12-.74.12-.972v-.804c-.76.064-1.36.132-1.8.204-1.52.272-2.28.916-2.28 1.932v.228z"/>',
			background: '#ff9900',
		},
	},
	{
		keywords: ['российские сервисы', 'россия', 'ркн', 'роскомнадзор', 'rkn', 'roskomnadzor'],
		config: {
			svg: '<path fill="white" fill-rule="evenodd" d="M765.748,167.568L598.331,0.151,425.5-.016-0.016,425.5v173L167.4,765.915,295.753,637.563,170.191,512,512,170.191,637.563,295.753Z"/><path fill="white" fill-rule="evenodd" d="M512.9,339.5l173,173L512.5,685.9l-173-173Z"/><path fill="white" fill-rule="evenodd" d="M258.252,856.432L425.669,1023.85l172.83,0.17L1024.02,598.5v-173L856.6,258.085,728.247,386.437,853.809,512,512,853.809,386.437,728.247Z"/>',
			background: '#0b4680',
			viewBox: '0 0 1024 1024',
			scale: 0.6,
		},
	},
	{
		keywords: ['torrent', 'торрент', 'торренты', 'torrents'],
		config: {
			svg: '<g transform="scale(2.0742981)"><path fill="white" fill-rule="evenodd" d="m 137.38383,239.99418 c 44.64832,-6.2824 81.43169,-37.04615 96.37094,-78.28306 -1.17843,0.43717 -2.61663,0.95348 -4.33344,1.46978 -20.0859,5.93191 -34.17804,-4.68447 -37.02056,-6.51981 -2.84253,-1.81651 -5.88835,-4.82769 -6.45686,-4.6694 -1.83729,11.38519 -12.22849,27.19481 -35.94756,34.36661 -12.98901,3.93827 -26.97196,4.02872 -36.94903,-1.56777 l 3.29055,8.26095 c 1.31773,3.29382 3.52774,8.69811 4.89817,11.97687 0,0 8.61793,20.53931 16.14779,34.96583"/><path fill="white" fill-rule="evenodd" d="M 27.337163,71.292153 62.88564,64.663041 c 3.237841,-0.591683 6.82582,1.247434 7.962829,4.085251 l 24.423106,61.011188 c 3.324434,6.36154 4.002122,7.76725 6.166955,10.48071 0,0 16.86313,24.09695 42.57008,18.21027 17.33374,-3.96466 25.42834,-16.98922 25.97802,-26.25641 0.5685,-2.97349 -0.32002,-6.71579 -1.86364,-10.20936 L 138.86947,55.784027 c -1.17465,-2.668226 0.21837,-5.28746 3.07219,-5.833919 l 29.64506,-5.528656 c 2.71828,-0.48616 5.90718,1.202209 7.09313,3.783756 l 32.401,69.437962 c 1.30643,2.77752 3.95318,7.15673 5.89964,9.70812 0,0 6.73923,9.68928 17.53705,7.85017 2.66934,0 5.7114,-1.44718 5.7114,-1.44718 0.47438,-4.32267 0.72663,-8.70565 0.72663,-13.16023 C 240.95557,53.986366 187.01165,0 120.46273,0 53.932634,0 0,53.986366 0,120.59405 c 0,53.44744 34.739017,98.7583 82.851068,114.57923 -3.249135,-6.69318 -6.53592,-14.0911 -9.227845,-21.31567 L 23.067731,77.872271 c -1.110654,-2.984796 0.835815,-5.935674 4.269432,-6.580118"/></g>',
			background: '#76b83f',
			viewBox: '0 0 500 498',
			scale: 0.6,
		},
	},
	{
		keywords: ['vk', 'вконтакте'],
		config: {
			svg: '<path fill="white" d="M21.547 7h-3.29a.743.743 0 0 0-.655.392s-1.312 2.416-1.734 3.23C14.734 12.813 14 12.126 14 11.11V7.603A1.104 1.104 0 0 0 12.896 6.5h-2.474a1.982 1.982 0 0 0-1.75.813s1.255-.204 1.255 1.49c0 .42.022 1.626.04 2.64a.73.73 0 0 1-1.272.503 21.54 21.54 0 0 1-2.498-4.543.693.693 0 0 0-.63-.403h-2.99a.508.508 0 0 0-.48.685C3.005 10.175 6.918 18 11.38 18h1.878a.742.742 0 0 0 .742-.742v-1.135a.73.73 0 0 1 1.23-.53l2.247 2.112a1.09 1.09 0 0 0 .746.295h2.953c1.424 0 1.424-.988.647-1.753-.546-.538-2.518-2.617-2.518-2.617a1.02 1.02 0 0 1-.078-1.323c.637-.84 1.68-2.212 2.122-2.8.603-.804 1.697-2.507.197-2.507z"/>',
			background: '#0077ff',
		},
	},
	{
		keywords: ['mail', 'почта'],
		config: {
			svg: '<rect x="2" y="4" width="20" height="16" rx="2" fill="none" stroke="white" stroke-width="2"/><path d="M2 7l10 6 10-6" fill="none" stroke="white" stroke-width="2"/>',
			background: '#005ff9',
		},
	},
	{
		keywords: ['tmdb', 'themoviedb'],
		config: {
			svg: '<rect x="3" y="5" width="18" height="14" rx="2" fill="none" stroke="white" stroke-width="1.8"/><rect x="5.5" y="7.5" width="5" height="4" rx="0.5" fill="white" opacity="0.9"/><rect x="13.5" y="7.5" width="5" height="4" rx="0.5" fill="white" opacity="0.9"/><rect x="5.5" y="13.5" width="5" height="3.5" rx="0.5" fill="white" opacity="0.5"/><rect x="13.5" y="13.5" width="5" height="3.5" rx="0.5" fill="white" opacity="0.5"/>',
			background: '#01b4e4',
		},
	},
];

const DEFAULT_ICON: ServiceIconConfig = {
	svg: '<circle cx="12" cy="12" r="10" fill="none" stroke="white" stroke-width="1.5"/><line x1="2" y1="12" x2="22" y2="12" stroke="white" stroke-width="1.5"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" fill="none" stroke="white" stroke-width="1.5"/>',
	background: '#292e42',
};

/** Preset ids with custom inline art in ICONS (not brandIcons). */
const PRESET_INLINE_SLUGS = new Set(['rkn', 'torrent', 'torrents', 'yandex', 'disney']);

export function isPresetInlineSlug(slug: string): boolean {
	return PRESET_INLINE_SLUGS.has(slug);
}

/** Substring match against service-icons keywords (e.g. "YouTube DISABLED" → youtube). */
export function hasServiceIconKeywordMatch(name: string): boolean {
	const lower = name.toLowerCase();
	return ICONS.some((entry) => entry.keywords.some((kw) => lower.includes(kw)));
}

export function getServiceIcon(name: string): ServiceIconConfig {
	const lower = name.toLowerCase();
	for (const entry of ICONS) {
		if (entry.keywords.some((kw) => lower.includes(kw))) {
			return entry.config;
		}
	}
	return DEFAULT_ICON;
}

/** Icon for PresetIcon by preset id (rkn, torrent, torrents). */
export function getPresetInlineIcon(slug: string): ServiceIconConfig | undefined {
	if (!isPresetInlineSlug(slug)) return undefined;
	return getServiceIcon(slug);
}
