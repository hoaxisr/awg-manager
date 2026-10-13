import type { MihomoSubscriptionFormat } from '$lib/types';

export type SupportedMihomoSubscriptionFormat = Exclude<MihomoSubscriptionFormat, 'auto'>;

/**
 * Legacy subscriptions could persist `auto`. The update endpoint accepts only
 * the concrete source formats, so retain the source meaning while migrating
 * the payload to the corresponding supported format.
 */
export function normalizeMihomoSubscriptionFormat(
	format: MihomoSubscriptionFormat,
	source: { url?: string; inline?: string },
): SupportedMihomoSubscriptionFormat {
	if (format !== 'auto') return format;
	return source.inline?.trim() ? 'share-links' : 'mihomo-provider';
}
