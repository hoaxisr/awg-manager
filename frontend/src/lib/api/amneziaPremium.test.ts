import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from './client';

/** Успешный конверт ручки: `request` разбирает его и валидирует по схеме. */
function envelope(data: unknown): Response {
	return new Response(JSON.stringify({ success: true, data }), {
		status: 200,
		headers: { 'Content-Type': 'application/json' },
	});
}

type Captured = { url: string; method: string; body: Record<string, unknown> };

/** Перехватывает единственный запрос и отдаёт его адрес, метод и разобранное тело. */
function captureFetch(data: unknown): () => Captured {
	let seen: Captured | null = null;
	globalThis.fetch = vi.fn().mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
		seen = {
			url: String(input),
			method: String(init?.method ?? 'GET'),
			body: init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : {},
		};
		return envelope(data);
	});
	return () => {
		if (!seen) throw new Error('запрос не ушёл');
		return seen;
	};
}

const KEY_STATE = { stored: true, usable: true, saveError: '' };

describe('ключ подписки Amnezia Premium', () => {
	const originalFetch = globalThis.fetch;

	beforeEach(() => {
		vi.restoreAllMocks();
	});

	afterEach(() => {
		globalThis.fetch = originalFetch;
	});

	// store и remember — разные флаги с разными умолчаниями: перепутать их
	// значит либо положить секрет на флеш без спроса, либо выдать себе
	// короткую сессию портала и ре-логин на каждом шаге мастера.
	it('по умолчанию у нас не сохраняет, а порталу говорит помнить сессию', async () => {
		const captured = captureFetch(KEY_STATE);

		await api.amneziaPremiumSaveKey('vpn://test-key-defaults');

		const req = captured();
		expect(req.url).toBe('/api/amnezia/premium/key');
		expect(req.method).toBe('POST');
		expect(req.body.store).toBe(false);
		expect(req.body.remember).toBe(true);
	});

	// Вторая половина той же проверки: с перепутанными полями оба значения
	// уезжают наоборот, и одного набора умолчаний для этого мало.
	it('явные значения уезжают каждое в своё поле', async () => {
		const captured = captureFetch(KEY_STATE);

		await api.amneziaPremiumSaveKey('vpn://test-key-explicit', { store: true, remember: false });

		const req = captured();
		expect(req.body.store).toBe(true);
		expect(req.body.remember).toBe(false);
	});

	it('обрезает пробелы вокруг ключа до отправки', async () => {
		const captured = captureFetch(KEY_STATE);

		await api.amneziaPremiumSaveKey('  \n vpn://test-key-padded \t ');

		expect(captured().body.key).toBe('vpn://test-key-padded');
	});

	it('состояние ключа читается GET-ом, забывается DELETE-ом', async () => {
		const getCaptured = captureFetch(KEY_STATE);
		await api.amneziaPremiumKeyState();
		expect(getCaptured().url).toBe('/api/amnezia/premium/key');
		expect(getCaptured().method).toBe('GET');

		const delCaptured = captureFetch({ stored: false, usable: false, saveError: '' });
		await api.amneziaPremiumForgetKey();
		expect(delCaptured().method).toBe('DELETE');
	});
});

describe('каталог, выдача и зеркало Amnezia Premium', () => {
	const originalFetch = globalThis.fetch;

	beforeEach(() => {
		vi.restoreAllMocks();
	});

	afterEach(() => {
		globalThis.fetch = originalFetch;
	});

	it('каталог читается нашей ручкой и приходит в нашей форме', async () => {
		const captured = captureFetch({
			planName: 'Premium',
			subscriptionEndDate: '2027-04-19T08:31:00Z',
			activeDeviceCount: 3,
			maxDeviceCount: 7,
			countries: [{ code: 'nl', name: 'Netherlands', protocols: ['awg'] }],
			issuedConfigs: [
				{
					countryCode: 'nl',
					lastIssuedAt: '2026-09-01T10:00:00Z',
					portalUpdatedAt: '2026-09-02T10:00:00Z',
					sourceType: 'downloaded_config',
				},
			],
		});

		const catalog = await api.amneziaPremiumCatalog();

		expect(captured().url).toBe('/api/amnezia/premium/catalog');
		expect(captured().method).toBe('GET');
		expect(catalog.countries[0].code).toBe('nl');
		expect(catalog.issuedConfigs?.[0].portalUpdatedAt).toBe('2026-09-02T10:00:00Z');
	});

	it('выдача конфигурации шлёт только код страны', async () => {
		const captured = captureFetch({ countryCode: 'nl', config: '[Interface]\n' });

		await api.amneziaPremiumConfig('nl');

		const req = captured();
		expect(req.url).toBe('/api/amnezia/premium/config');
		expect(req.method).toBe('POST');
		expect(req.body).toEqual({ countryCode: 'nl' });
	});

	it('адрес зеркала читается и записывается одной ручкой', async () => {
		const getCaptured = captureFetch({ mirrorUrl: 'https://mirror.test/cp' });
		await api.amneziaPremiumMirror();
		expect(getCaptured().url).toBe('/api/amnezia/premium/mirror');
		expect(getCaptured().method).toBe('GET');

		const postCaptured = captureFetch({ mirrorUrl: 'https://other.test/cp' });
		await api.amneziaPremiumSaveMirror('https://other.test/cp');
		expect(postCaptured().method).toBe('POST');
		expect(postCaptured().body).toEqual({ mirrorUrl: 'https://other.test/cp' });
	});
});

describe('замена конфигурации и метка страны подписки', () => {
	const originalFetch = globalThis.fetch;

	beforeEach(() => {
		vi.restoreAllMocks();
	});

	afterEach(() => {
		globalThis.fetch = originalFetch;
	});

	// Замена файлом страны не знает, и прежняя метка обязана исчезнуть.
	// «Очистить» на проводе — это ПРИСЛАННОЕ пустое значение, а не
	// пропущенное поле: пропуск читался бы как «страну не трогать».
	it('без страны шлёт пустую метку, а не пропускает поле', async () => {
		const captured = captureFetch({ id: 'awg10', name: 'т8' });

		await api.replaceConfig('awg10', '[Interface]\nAddress = 10.0.0.2/32\n');

		const req = captured();
		expect(Object.prototype.hasOwnProperty.call(req.body, 'amneziaCountry')).toBe(true);
		expect(req.body.amneziaCountry).toBe('');
	});

	it('со страной шлёт именно её', async () => {
		const captured = captureFetch({ id: 'awg10', name: 'т8' });

		await api.replaceConfig('awg10', '[Interface]\n', 'т8-nl', 'nl');

		const req = captured();
		expect(req.body.amneziaCountry).toBe('nl');
		expect(req.body.name).toBe('т8-nl');
	});
});
