import type { FircCatalog } from '$lib/types/firc';

export interface FircCatalogMatch {
	name: string;
	source: string;
	url: string;
}

export interface FircCatalogSearchResult {
	items: FircCatalogMatch[];
	total: number;
}

function isTokenSep(ch: string | undefined): boolean {
	return (
		ch === '-' ||
		ch === '_' ||
		ch === '@' ||
		ch === '.' ||
		ch === '/' ||
		ch === '!' ||
		ch === ':'
	);
}

function scoreTerm(lower: string, term: string): number {
	if (lower === term) return 0;
	let best = Infinity;
	let idx = lower.indexOf(term);
	while (idx !== -1) {
		const end = idx + term.length;
		const leftBound = idx === 0 || isTokenSep(lower[idx - 1]);
		const rightBound = end === lower.length || isTokenSep(lower[end]);
		let cand = 6;
		if (leftBound && rightBound) {
			if (idx === 0) cand = 1;
			else if (end === lower.length) cand = 2;
			else cand = 3;
		} else if (idx === 0) {
			cand = 4;
		} else if (leftBound) {
			cand = 5;
		}
		if (cand < best) best = cand;
		if (best === 1) break;
		idx = lower.indexOf(term, idx + 1);
	}
	return best === Infinity ? -1 : best;
}

export function searchFircCatalog(
	catalog: FircCatalog | null | undefined,
	rawQuery: string,
	limit = 100,
): FircCatalogSearchResult {
	if (!catalog?.sources?.length) {
		return { items: [], total: 0 };
	}
	const q = rawQuery.trim().toLowerCase();
	if (!q || q.includes('://')) {
		return { items: [], total: 0 };
	}
	const terms = q.split(/\s+/).filter(Boolean);
	if (terms.length === 0) {
		return { items: [], total: 0 };
	}

	interface Candidate extends FircCatalogMatch {
		score: number;
		sourceIndex: number;
	}

	const byUrl = new Map<string, Candidate>();
	for (let sourceIndex = 0; sourceIndex < catalog.sources.length; sourceIndex++) {
		const src = catalog.sources[sourceIndex];
		if (!src) continue;
		for (const name of src.names ?? []) {
			const lower = name.toLowerCase();
			let score = 0;
			if (terms.length === 1) {
				score = scoreTerm(lower, terms[0]);
				if (score < 0) continue;
			} else {
				let matched = true;
				for (const term of terms) {
					const termScore = scoreTerm(lower, term);
					if (termScore < 0) {
						matched = false;
						break;
					}
					score += termScore;
				}
				if (!matched) continue;
			}
			const url = src.url.replace('{}', () => name);
			const existing = byUrl.get(url);
			if (!existing || score < existing.score) {
				byUrl.set(url, {
					name,
					source: src.id,
					url,
					score,
					sourceIndex,
				});
			}
		}
	}

	const sorted = [...byUrl.values()].sort(
		(a, b) =>
			a.score - b.score ||
			a.name.length - b.name.length ||
			a.name.localeCompare(b.name) ||
			a.sourceIndex - b.sourceIndex,
	);

	return {
		items: sorted.slice(0, Math.max(0, limit)).map(({ name, source, url }) => ({
			name,
			source,
			url,
		})),
		total: sorted.length,
	};
}
