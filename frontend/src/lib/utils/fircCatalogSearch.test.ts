import { describe, it, expect } from 'vitest';
import type { FircCatalog } from '$lib/types/firc';
import { searchFircCatalog } from './fircCatalogSearch';

function makeCatalog(): FircCatalog {
	return {
		generated: '2026-01-01T00:00:00Z',
		fetchedAt: 1700000000,
		stale: false,
		sources: [
			{
				id: 'sagernet',
				url: 'https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-{}.srs',
				names: [
					'aruba',
					'beeline-ru',
					'carrefour',
					'category-ads-ru',
					'category-ai-ru',
					'category-dev-ru',
					'category-ecommerce-ru',
					'category-education-ru',
					'category-entertainment-ru',
					'category-finance-ru',
					'category-forums-ru',
					'category-games-ru',
					'category-gov-ru',
					'category-health-ru',
					'category-housing-ru',
					'category-jobs-ru',
					'category-maps-ru',
					'category-media-ru',
					'category-media-ru-blocked',
					'category-medicine-ru',
					'category-news-ru',
					'category-realty-ru',
					'category-retail-ru',
					'category-retail-ru@ads',
					'category-science-ru',
					'category-social-ru',
					'category-sport-ru',
					'category-telecom-ru',
					'category-travel-ru',
					'category-ru',
					'category-ru-blocked',
					'category-ru@ads',
					'crunchyroll',
					'drupal',
					'forum',
					'ru',
					'rutube',
					'category-rutube',
				],
			},
			{
				id: 'runetfreedom',
				url: 'https://raw.githubusercontent.com/runetfreedom/russia-v2ray-rules-dat/release/{}.srs',
				names: ['ru-blocked', 'category-ru-extra'],
			},
		],
	};
}

describe('searchFircCatalog', () => {
	it('reproduces the Ru cutoff bug and ranks exact/token hits from all sources first', () => {
		const res = searchFircCatalog(makeCatalog(), 'Ru');
		const names = res.items.map((x) => x.name);

		expect(res.total).toBe(39);
		expect(names).not.toContain('carrefour');
		expect(names[0]).toBe('ru');

		const idxRuBlocked = names.indexOf('ru-blocked');
		const idxCategoryRu = names.indexOf('category-ru');
		const idxCategoryMediaRu = names.indexOf('category-media-ru');
		const idxCategoryRetailRu = names.indexOf('category-retail-ru');
		const idxCategoryRuAds = names.indexOf('category-ru@ads');
		const idxRutube = names.indexOf('rutube');
		const idxCategoryRutube = names.indexOf('category-rutube');
		const idxDrupal = names.indexOf('drupal');
		const idxAruba = names.indexOf('aruba');
		const idxForum = names.indexOf('forum');

		expect(idxRuBlocked).toBeGreaterThan(0);
		expect(idxCategoryRu).toBeGreaterThan(0);
		expect(idxCategoryRu).toBeLessThan(idxCategoryMediaRu);
		expect(idxCategoryRu).toBeLessThan(idxCategoryRetailRu);
		expect(idxCategoryMediaRu).toBeLessThan(idxCategoryRuAds);
		expect(idxCategoryRuAds).toBeLessThan(idxRutube);
		expect(idxRutube).toBeLessThan(idxCategoryRutube);
		expect(idxCategoryRutube).toBeLessThan(idxAruba);
		expect(idxCategoryRutube).toBeLessThan(idxDrupal);
		expect(idxCategoryRutube).toBeLessThan(idxForum);

		const secondSourceHit = res.items.find((x) => x.name === 'category-ru-extra');
		expect(secondSourceHit).toEqual({
			name: 'category-ru-extra',
			source: 'runetfreedom',
			url: 'https://raw.githubusercontent.com/runetfreedom/russia-v2ray-rules-dat/release/category-ru-extra.srs',
		});
	});

	it('places exact category-ru match ahead of suffix and prefix variants', () => {
		const res = searchFircCatalog(makeCatalog(), 'category-ru');
		expect(res.items.map((x) => x.name)).toEqual([
			'category-ru',
			'category-ru@ads',
			'category-ru-extra',
			'category-ru-blocked',
			'category-rutube',
		]);
	});

	it('supports multi-word queries and ranks shorter exact token hits higher', () => {
		const res = searchFircCatalog(makeCatalog(), 'cat ru');
		expect(res.items[0]?.name).toBe('category-ru');
		const idxCategoryRu = res.items.findIndex((x) => x.name === 'category-ru');
		const idxCategoryMediaRu = res.items.findIndex((x) => x.name === 'category-media-ru');
		expect(idxCategoryRu).toBe(0);
		expect(idxCategoryMediaRu).toBeGreaterThan(idxCategoryRu);
	});

	it('returns empty results for empty/whitespace queries and full URLs', () => {
		const cat = makeCatalog();
		expect(searchFircCatalog(cat, '')).toEqual({ items: [], total: 0 });
		expect(searchFircCatalog(cat, '   ')).toEqual({ items: [], total: 0 });
		expect(
			searchFircCatalog(
				cat,
				'https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ru.srs',
			),
		).toEqual({ items: [], total: 0 });
	});

	it('respects custom limit while reporting accurate total across all sources', () => {
		const res = searchFircCatalog(makeCatalog(), 'ru', 5);
		expect(res.items).toHaveLength(5);
		expect(res.total).toBe(39);
		expect(res.items.map((x) => x.name)).toContain('category-ru');
	});
});
