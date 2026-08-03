import { goto } from '$app/navigation';
import { page } from '$app/stores';
import { get } from 'svelte/store';

/**
 * Read tab from current URL.
 */
export function readTabParam(urlParam = 'tab'): string | null {
	return get(page).url.searchParams.get(urlParam);
}

/**
 * Write active tab to URL (replaceState). Removes param when value === defaultTab.
 * Preserves other search params.
 */
export function writeTabParam(
	value: string,
	opts: { urlParam?: string; defaultTab: string },
): void {
	const urlParam = opts.urlParam ?? 'tab';
	const current = get(page).url;
	const url = new URL(current);

	if (value === opts.defaultTab) {
		url.searchParams.delete(urlParam);
	} else {
		url.searchParams.set(urlParam, value);
	}

	const nextSearch = url.searchParams.toString();
	const currentSearch = current.searchParams.toString();
	if (
		url.pathname === current.pathname &&
		nextSearch === currentSearch &&
		url.hash === current.hash
	) {
		return;
	}

	const target = url.pathname + (nextSearch ? `?${nextSearch}` : '') + url.hash;
	void goto(target, { replaceState: true, keepFocus: true, noScroll: true });
}

/**
 * Bidirectional ?tab= sync for Svelte 5 pages (mirrors Tabs.svelte urlParam behavior).
 *
 * @example
 * // Inbound: URL → activeTab (browser back, sidebar goto, deep links).
 * // Outbound: activeTab → URL (programmatic changes, bounce-off hidden tabs).
 * // Use untrack(() => activeTab) in inbound to avoid racing outbound.
 * // Hold outbound (urlConsumed=false) until deep-linked id is in the tab list.
 */
