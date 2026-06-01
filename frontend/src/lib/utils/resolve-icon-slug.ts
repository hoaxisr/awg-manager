import { brandIcons } from '$lib/generated/brandIcons';
import { SERVICE_PRESETS } from '$lib/data/presets';
import { isPresetInlineSlug } from '$lib/utils/service-icons';

const LUCIDE_SLUGS = new Set([
	'lucide-circle-slash',
	'lucide-globe',
	'lucide-sparkles',
	'lucide-film',
	'lucide-gamepad-2',
	'lucide-shield-check',
	'lucide-shield-alert',
	'lucide-cpu',
	'lucide-eye-off',
	'lucide-lock',
	'lucide-briefcase-business',
	'lucide-shield-off',
	'lucide-globe-lock',
	'lucide-list',
]);

/** Name aliases → sing-box preset iconSlug (brandIcons / lucide). */
const NAME_ALIASES: Record<string, string> = {
	'twitter/x': 'x',
	twitter: 'x',
	'google gemini': 'googlegemini',
	'google-gemini': 'googlegemini',
	gemini: 'googlegemini',
	торренты: 'torrents',
	'заблокировано в рф': 'rkn',
	'реклама и трекеры': 'lucide-circle-slash',
	'недоступно из рф': 'lucide-globe-lock',
	atlassian: 'atlassian',
	bitbucket: 'atlassian',
	jira: 'atlassian',
	confluence: 'atlassian',
	ubisoft: 'ubisoft',
	yandex: 'yandex',
	яндекс: 'yandex',
	disney: 'disney',
	'disney+': 'disney',
};

function normalizeKey(s: string): string {
	return s.trim().toLowerCase().replace(/^geo(site|ip)[-_]/, '');
}

/** True when PresetIcon can render this slug (not the "?" fallback). */
export function isPresetIconResolvable(slug: string): boolean {
	if (!slug) return false;
	if (isPresetInlineSlug(slug) || slug === 'instagram' || LUCIDE_SLUGS.has(slug)) return true;
	return slug in brandIcons;
}

/**
 * Resolve iconSlug for PresetIcon from explicit slug, preset id, or rule name.
 * Matches sing-box / unified routing catalog conventions.
 */
export function resolveIconSlug(name: string, iconSlug?: string): string | undefined {
	if (iconSlug && isPresetIconResolvable(iconSlug)) return iconSlug;

	const key = normalizeKey(name);
	if (!key) return undefined;

	if (NAME_ALIASES[key] && isPresetIconResolvable(NAME_ALIASES[key])) {
		return NAME_ALIASES[key];
	}

	for (const preset of SERVICE_PRESETS) {
		if (normalizeKey(preset.id) === key || normalizeKey(preset.name) === key) {
			if (isPresetIconResolvable(preset.id)) return preset.id;
		}
	}

	const compact = key.replace(/[^a-z0-9]+/g, '');
	if (compact && isPresetIconResolvable(compact)) return compact;

	return undefined;
}
