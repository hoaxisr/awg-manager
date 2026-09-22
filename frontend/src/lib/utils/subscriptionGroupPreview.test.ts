import { describe, it, expect } from 'vitest';
import { resolveGroupPreview, softCompileGoRegex } from './subscriptionGroupPreview';
import type { Subscription, SubscriptionMember } from '$lib/types';

function member(tag: string, label: string): SubscriptionMember {
	return { tag, label, protocol: 'vless', server: `${tag}.example`, port: 443 };
}

function sub(id: string, enabled: boolean, members: SubscriptionMember[]): Subscription {
	return {
		id,
		label: id,
		url: `https://example.com/${id}`,
		path: '',
		isInline: false,
		isFile: false,
		headers: [],
		refreshHours: 0,
		lastFetched: '',
		selectorTag: `sub-${id}`,
		inboundTag: `sub-${id}-in`,
		listenPort: 11000,
		proxyIndex: -1,
		memberTags: members.map((m) => m.tag),
		members,
		orphanTags: [],
		activeMember: members[0]?.tag ?? '',
		enabled,
		mode: 'selector',
	};
}

const subs: Subscription[] = [
	sub('a', true, [member('a1', '🇩🇪 DE-1'), member('a2', '🇷🇺 RU-1')]),
	sub('b', true, [member('b1', 'NL-1'), member('b2', 'NL-2')]),
	sub('off', false, [member('o1', 'DE-off')]),
];

describe('resolveGroupPreview', () => {
	it('объединяет подписки в порядке useSubscriptionIds', () => {
		const res = resolveGroupPreview(subs, ['b', 'a'], '', '');
		expect(res.count).toBe(4);
		expect(res.labels).toEqual(['NL-1', 'NL-2', '🇩🇪 DE-1', '🇷🇺 RU-1']);
	});

	it('пропускает выключенные и несуществующие подписки', () => {
		const res = resolveGroupPreview(subs, ['off', 'missing', 'a'], '', '');
		expect(res.labels).toEqual(['🇩🇪 DE-1', '🇷🇺 RU-1']);
	});

	it('схлопывает дубликаты id подписок', () => {
		const res = resolveGroupPreview(subs, ['a', 'a'], '', '');
		expect(res.count).toBe(2);
	});

	it('применяет include и exclude вместе', () => {
		const res = resolveGroupPreview(subs, ['a', 'b'], 'DE|NL', 'NL-2');
		expect(res.labels).toEqual(['🇩🇪 DE-1', 'NL-1']);
	});

	it('фильтрует по эмодзи-флагам', () => {
		const res = resolveGroupPreview(subs, ['a'], '', '🇷🇺');
		expect(res.labels).toEqual(['🇩🇪 DE-1']);
	});

	it('флагует некомпилируемый шаблон и не ограничивает по нему', () => {
		const res = resolveGroupPreview(subs, ['b'], '(broken', '');
		expect(res.invalidInclude).toBe(true);
		expect(res.lookaroundInclude).toBe(false);
		expect(res.count).toBe(2); // битый include игнорируется в превью
	});

	it('ведущий (?i) фильтрует без учёта регистра (Go-плейсхолдер валиден)', () => {
		// '(?i)(de|nl)' совпадает с 'DE-1' и 'NL-*' несмотря на разный регистр.
		const res = resolveGroupPreview(subs, ['a', 'b'], '(?i)(de|nl)', '');
		expect(res.invalidInclude).toBe(false);
		expect(res.lookaroundInclude).toBe(false);
		expect(res.labels).toEqual(['🇩🇪 DE-1', 'NL-1', 'NL-2']);
	});

	it('lookahead флагуется отдельно от invalid и не ограничивает превью', () => {
		const res = resolveGroupPreview(subs, ['b'], '(?!RU)', '');
		expect(res.lookaroundInclude).toBe(true);
		expect(res.invalidInclude).toBe(false);
		expect(res.count).toBe(2); // lookaround игнорируется в превью
	});

	it('пустое имя сервера подставляет server:port', () => {
		const withEmpty = [sub('c', true, [member('c1', '')])];
		const res = resolveGroupPreview(withEmpty, ['c'], '', '');
		expect(res.labels).toEqual(['c1.example:443']);
	});
});

describe('softCompileGoRegex', () => {
	it('срезает ведущий (?i) и компилирует с флагом i', () => {
		const res = softCompileGoRegex('(?i)(DE|NL|🇩🇪)');
		expect(res.invalid).toBe(false);
		expect(res.lookaround).toBe(false);
		expect(res.re?.test('de-frankfurt')).toBe(true);
		expect(res.re?.test('nl-1')).toBe(true);
		expect(res.re?.test('ru-1')).toBe(false);
	});

	it('детектирует все lookaround-токены (список как в backend filter.go)', () => {
		for (const p of ['(?=x)', '(?!x)', '(?<=x)', '(?<!x)', '(?i)^(?!.*(RU|Russia)).*$']) {
			const res = softCompileGoRegex(p);
			expect(res.lookaround).toBe(true);
			expect(res.invalid).toBe(false);
			expect(res.re).toBeNull();
		}
	});

	it('именованная группа (?<name>) НЕ считается lookaround', () => {
		const res = softCompileGoRegex('(?<country>DE|NL)');
		expect(res.lookaround).toBe(false);
		expect(res.invalid).toBe(false);
		expect(res.re?.test('DE-1')).toBe(true);
	});

	it('прочие некомпилируемые шаблоны остаются invalid', () => {
		const res = softCompileGoRegex('(broken');
		expect(res.invalid).toBe(true);
		expect(res.lookaround).toBe(false);
		expect(res.re).toBeNull();
	});

	it('пустой шаблон и голый (?i) — без ограничения и без флагов', () => {
		expect(softCompileGoRegex('')).toEqual({ re: null, invalid: false, lookaround: false });
		expect(softCompileGoRegex('(?i)')).toEqual({ re: null, invalid: false, lookaround: false });
	});
});
