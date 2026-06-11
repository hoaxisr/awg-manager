import type { SingboxRouterRuleSet } from '$lib/types';
import type { BadgeVariant } from '$lib/components/ui/Badge.svelte';

export type RuleSetDisplayType = 'inline' | 'remote' | 'local' | 'dat';

export interface RuleSetDatInfo {
	kind: 'geosite' | 'geoip';
	tags: string[];
}

export function datInfo(rs: SingboxRouterRuleSet): RuleSetDatInfo | null {
	if (rs.type !== 'remote' || !rs.url) return null;
	try {
		const u = new URL(rs.url);
		if (u.pathname !== '/api/singbox/router/rulesets/dat-srs') return null;
		const kind = u.searchParams.get('kind');
		const tags = u.searchParams.getAll('tag').filter((t) => t.trim() !== '');
		if ((kind === 'geosite' || kind === 'geoip') && tags.length > 0) {
			return { kind, tags };
		}
		return null;
	} catch {
		return null;
	}
}

export function resolveRuleSetDisplayType(rs: SingboxRouterRuleSet): RuleSetDisplayType {
	if (datInfo(rs)) return 'dat';
	return (rs.type ?? 'remote') as Exclude<RuleSetDisplayType, 'dat'>;
}

export function ruleSetDisplayLabel(type: RuleSetDisplayType): string {
	return type;
}

export function ruleSetDisplayVariant(type: RuleSetDisplayType): BadgeVariant {
	if (type === 'local') return 'info';
	if (type === 'inline') return 'warning';
	if (type === 'dat') return 'purple';
	return 'accent';
}
