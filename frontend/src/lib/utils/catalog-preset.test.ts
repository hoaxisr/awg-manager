import { describe, expect, it } from 'vitest';
import type { CatalogPreset } from '$lib/types';
import {
	DNS_LARGE_LIST_THRESHOLD,
	dnsLargeListNotice,
	applyPresetToggle,
	catalogCardMarker,
	catalogPresetCardNotice,
	dnsRouteCatalogPresetFilter,
	findAddedCompositeForMember,
	findCoveringPreset,
	hrNeoCatalogPresetFilter,
	isPresetFullyAdded,
	memberOfAddedCompositeTitle,
	normalizeCatalogSelection,
	presetAddedBadge,
	presetDnsLargeListRisk,
	presetSetReuseBadge,
	resolvePresetDnsEntries,
	resolvePresetManualDomains,
	singboxRouterCatalogPresetFilter,
	splitPresetDnsEntries,
	addedCompositesByMember,
} from './catalog-preset';

const base = {
	id: 'x',
	name: 'X',
	iconSlug: 'x',
	category: 'media',
	origin: 'builtin' as const,
	engines: {},
};

describe('splitPresetDnsEntries', () => {
	it('maps domains and subnets arrays to HR editor fields', () => {
		const p: CatalogPreset = {
			...base,
			engines: {
				dns: {
					domains: ['example.com', 'geoip:ru'],
					subnets: ['91.108.4.0/22', '10.0.0.0/8'],
				},
			},
		};
		expect(splitPresetDnsEntries(p)).toEqual({
			domainLines: ['example.com'],
			cidrLines: ['geoip:ru', '91.108.4.0/22', '10.0.0.0/8'],
		});
	});
});

describe('hrNeoCatalogPresetFilter', () => {
	it('accepts subnet-only presets', () => {
		const p: CatalogPreset = {
			...base,
			engines: { dns: { subnets: ['10.0.0.0/8'] } },
		};
		expect(hrNeoCatalogPresetFilter(p)).toBe(true);
	});

	it('accepts composite parent with empty own DNS via covers', () => {
		const catalog: CatalogPreset[] = [
			{
				...base,
				id: 'meta',
				covers: ['instagram'],
				engines: { singbox: { action: 'tunnel', ruleSets: [] } },
			},
			{
				...base,
				id: 'instagram',
				engines: { dns: { domains: ['instagram.com'] } },
			},
		];
		expect(hrNeoCatalogPresetFilter(catalog[0], catalog)).toBe(true);
		expect(dnsRouteCatalogPresetFilter(catalog[0], catalog)).toBe(true);
	});
});

describe('resolvePresetDnsEntries', () => {
	const catalog: CatalogPreset[] = [
		{
			...base,
			id: 'meta',
			covers: ['instagram', 'whatsapp'],
			engines: {},
		},
		{
			...base,
			id: 'instagram',
			engines: { dns: { domains: ['instagram.com', 'cdninstagram.com'] } },
		},
		{
			...base,
			id: 'whatsapp',
			engines: { dns: { domains: ['whatsapp.com'], subnets: ['91.108.0.0/16'] } },
		},
	];

	it('expands empty parent to union of covered children', () => {
		expect(resolvePresetDnsEntries(catalog[0], catalog)).toEqual({
			domainLines: ['cdninstagram.com', 'instagram.com', 'whatsapp.com'],
			cidrLines: ['91.108.0.0/16'],
		});
	});

	it('merges own domains with covered children', () => {
		const parent: CatalogPreset = {
			...base,
			id: 'bundle',
			covers: ['instagram'],
			engines: { dns: { domains: ['meta.com'] } },
		};
		const cat = [...catalog.slice(1), parent];
		expect(resolvePresetManualDomains(parent, cat)).toEqual([
			'cdninstagram.com',
			'instagram.com',
			'meta.com',
		]);
	});
});

describe('presetDnsLargeListRisk', () => {
	it('does not flag subscription-only lists', () => {
		const p: CatalogPreset = {
			...base,
			engines: { dns: { subscriptionUrl: 'https://example.com/list.txt' } },
		};
		expect(presetDnsLargeListRisk(p)).toBe(false);
	});

	it(`flags inline lists above ${DNS_LARGE_LIST_THRESHOLD}`, () => {
		const domains = Array.from({ length: DNS_LARGE_LIST_THRESHOLD + 1 }, (_, i) => `d${i}.com`);
		const p: CatalogPreset = { ...base, engines: { dns: { domains } } };
		expect(presetDnsLargeListRisk(p)).toBe(true);
	});

	it('ignores small inline lists', () => {
		const p: CatalogPreset = { ...base, engines: { dns: { domains: ['a.com'] } } };
		expect(presetDnsLargeListRisk(p)).toBe(false);
	});
});

describe('catalogPresetCardNotice', () => {
	it('includes large-list notice for NDMS picker', () => {
		const p: CatalogPreset = {
			...base,
			notice: 'Sensitive',
			engines: {
				dns: {
					domains: Array.from(
						{ length: DNS_LARGE_LIST_THRESHOLD + 1 },
						(_, i) => `x${i}.com`,
					),
				},
			},
		};
		const text = catalogPresetCardNotice(p, true);
		expect(text).toContain(dnsLargeListNotice());
		expect(text).toContain('Sensitive');
	});

	it('omits large-list notice when disabled (sing-box catalog)', () => {
		const p: CatalogPreset = {
			...base,
			engines: { dns: { domains: Array.from({ length: 250 }, (_, i) => `x${i}.com`) } },
		};
		expect(catalogPresetCardNotice(p, false)).toBeUndefined();
	});
});

describe('singboxRouterCatalogPresetFilter', () => {
	it('accepts presets with singbox engine', () => {
		const p: CatalogPreset = {
			...base,
			engines: { singbox: { action: 'route', ruleSets: [] } },
		};
		expect(singboxRouterCatalogPresetFilter(p)).toBe(true);
	});

	it('rejects dns-only presets', () => {
		const p: CatalogPreset = {
			...base,
			engines: { dns: { domains: ['a.com'] } },
		};
		expect(singboxRouterCatalogPresetFilter(p)).toBe(false);
	});
});

describe('catalog covers selection', () => {
	const catalog: CatalogPreset[] = [
		{ ...base, id: 'meta', name: 'Meta', covers: ['instagram', 'whatsapp'], engines: {} },
		{ ...base, id: 'instagram', name: 'Instagram', engines: {} },
		{ ...base, id: 'whatsapp', name: 'WhatsApp', engines: {} },
	];

	it('drops covered children when parent is selected', () => {
		const selected = new Set(['instagram', 'whatsapp']);
		const next = applyPresetToggle(selected, 'meta', catalog, true);
		expect([...next]).toEqual(['meta']);
	});

	it('normalizes an existing parent+child selection', () => {
		const next = normalizeCatalogSelection(new Set(['meta', 'instagram']), catalog);
		expect([...next]).toEqual(['meta']);
	});

	it('finds covering preset for a child', () => {
		const parent = findCoveringPreset('instagram', new Set(['meta']), catalog);
		expect(parent?.id).toBe('meta');
	});
});

function sbPreset(
	id: string,
	name: string,
	tags: string[],
	covers?: string[],
): CatalogPreset {
	return {
		...base,
		id,
		name,
		covers,
		engines: {
			singbox: {
				action: 'tunnel',
				ruleSets: tags.map((tag) => ({ tag, url: `https://example.com/${tag}.srs` })),
			},
		},
	};
}

describe('findAddedCompositeForMember (#450)', () => {
	const composite = sbPreset(
		'category-ai',
		'Все AI сервисы',
		['geosite-category-ai-!cn'],
		['anthropic', 'openai'],
	);
	const anthropic = sbPreset('anthropic', 'Anthropic', ['geosite-anthropic']);
	const catalog = [composite, anthropic];

	it('returns composite when its own rule sets are fully added', () => {
		const found = findAddedCompositeForMember(
			'anthropic',
			catalog,
			new Set(['geosite-category-ai-!cn']),
		);
		expect(found?.id).toBe('category-ai');
	});

	it('ignores composites whose rule sets are not fully added', () => {
		expect(
			findAddedCompositeForMember('anthropic', catalog, new Set(['geosite-anthropic'])),
		).toBeUndefined();
	});

	it('ignores composites without singbox rule sets (empty covers source)', () => {
		const dnsComposite: CatalogPreset = {
			...base,
			id: 'dns-bundle',
			covers: ['anthropic'],
			engines: { dns: { domains: ['a.com'] } },
		};
		expect(
			findAddedCompositeForMember('anthropic', [dnsComposite, anthropic], new Set(['x'])),
		).toBeUndefined();
	});

	it('returns undefined for non-members and empty covers', () => {
		const noCovers = sbPreset('solo', 'Solo', ['geosite-solo']);
		const tags = new Set(['geosite-category-ai-!cn', 'geosite-solo']);
		expect(findAddedCompositeForMember('openai-x', catalog, tags)).toBeUndefined();
		expect(findAddedCompositeForMember('anthropic', [noCovers], tags)).toBeUndefined();
	});

	it('first-wins is deterministic over the passed catalog order', () => {
		// Два ДОБАВЛЕННЫХ композита покрывают одного участника: побеждает первый
		// в переданном (отсортированном вызывающими) порядке каталога.
		const compA = sbPreset('comp-a', 'А-композит', ['geosite-comp-a'], ['member-x']);
		const compB = sbPreset('comp-b', 'Б-композит', ['geosite-comp-b'], ['member-x']);
		const tags = new Set(['geosite-comp-a', 'geosite-comp-b']);
		expect(addedCompositesByMember([compA, compB], tags).get('member-x')?.id).toBe('comp-a');
		expect(addedCompositesByMember([compB, compA], tags).get('member-x')?.id).toBe('comp-b');
	});

	it('never reports a composite as covering itself', () => {
		const selfRef = sbPreset('weird', 'Weird', ['geosite-weird'], ['weird']);
		expect(
			findAddedCompositeForMember('weird', [selfRef], new Set(['geosite-weird'])),
		).toBeUndefined();
	});
});

describe('isPresetFullyAdded', () => {
	it('requires a non-empty rule set list with every tag present', () => {
		const p = sbPreset('netflix', 'Netflix', ['geosite-netflix', 'geoip-netflix']);
		expect(isPresetFullyAdded(p, new Set(['geosite-netflix', 'geoip-netflix']))).toBe(true);
		expect(isPresetFullyAdded(p, new Set(['geosite-netflix']))).toBe(false);
		expect(isPresetFullyAdded({ ...base, engines: {} }, new Set(['x']))).toBe(false);
	});
});

describe('catalogCardMarker precedence ladder', () => {
	const none = {
		added: false,
		coveredBySelection: false,
		ownSetAdded: false,
		memberOfAddedComposite: false,
	};

	it('added (own tag in config) wins over everything', () => {
		expect(
			catalogCardMarker({
				added: true,
				coveredBySelection: true,
				ownSetAdded: true,
				memberOfAddedComposite: true,
			}),
		).toBe('added');
	});

	it('in-session covered wins over set-added and member marks', () => {
		expect(
			catalogCardMarker({
				...none,
				coveredBySelection: true,
				ownSetAdded: true,
				memberOfAddedComposite: true,
			}),
		).toBe('covered');
	});

	it('member itself added takes precedence over member-of-added mark', () => {
		expect(
			catalogCardMarker({ ...none, ownSetAdded: true, memberOfAddedComposite: true }),
		).toBe('own-set-added');
	});

	it('member-of-added only when nothing else applies', () => {
		expect(catalogCardMarker({ ...none, memberOfAddedComposite: true })).toBe(
			'member-of-added',
		);
		expect(catalogCardMarker(none)).toBe('none');
	});
});

describe('presetAddedBadge', () => {
	const netflix = sbPreset('netflix', 'Netflix', ['geosite-netflix']);

	it('falls back to plain «добавлено» without usage info', () => {
		expect(presetAddedBadge(netflix, undefined)).toEqual({ text: 'добавлено' });
	});

	it('reports a set referenced by at least one rule as used', () => {
		const badge = presetAddedBadge(netflix, new Map([['geosite-netflix', 2]]));
		expect(badge.text).toBe('добавлено');
		expect(badge.tooltip).toContain('используется правилами');
	});

	it('flags a set not referenced by any rule', () => {
		const badge = presetAddedBadge(netflix, new Map([['other-tag', 1]]));
		expect(badge.text).toBe('добавлено, без правил');
		expect(badge.tooltip).toBe('Добавлен как набор — не используется ни одним правилом');
	});

	it('multi-set preset counts as used when any of its tags is referenced', () => {
		const p = sbPreset('multi', 'Multi', ['a', 'b']);
		expect(presetAddedBadge(p, new Map([['b', 1]])).text).toBe('добавлено');
	});

	it('preset without singbox tags falls back to plain «добавлено»', () => {
		expect(presetAddedBadge({ ...base, engines: {} }, new Map())).toEqual({
			text: 'добавлено',
		});
	});
});

describe('wizard reuse badge and member mark tooltip', () => {
	it('presetSetReuseBadge explains that only a rule will be created', () => {
		expect(presetSetReuseBadge()).toEqual({
			text: 'набор уже есть',
			tooltip: 'Набор уже добавлен — будет создано только правило',
		});
	});

	it('memberOfAddedCompositeTitle names the composite and stays non-blocking', () => {
		const title = memberOfAddedCompositeTitle('Все AI сервисы');
		expect(title).toBe(
			'Уже входит в добавленный композитный список «Все AI сервисы». Можно добавить и отдельно.',
		);
	});
});
