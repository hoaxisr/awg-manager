import type { WizardGuideItem } from '$lib/components/proxy-panel/ProxyWizardGuide.svelte';

export type { WizardGuideItem };

export function guide(
	id: string,
	text: string,
	opts: { done?: boolean; active?: boolean; pending?: boolean } = {}
): WizardGuideItem {
	return { id, text, ...opts };
}

/** Первый незавершённый пункт без pending становится active, если active не задан явно. */
export function finalizeGuide(items: WizardGuideItem[]): WizardGuideItem[] {
	const out = items.map((it) => ({ ...it }));
	if (out.some((i) => i.active)) return out;
	const idx = out.findIndex((i) => !i.done && !i.pending);
	if (idx >= 0) out[idx].active = true;
	else {
		for (let i = out.length - 1; i >= 0; i--) {
			if (!out[i].done) {
				out[i].active = true;
				break;
			}
		}
	}
	return out;
}
