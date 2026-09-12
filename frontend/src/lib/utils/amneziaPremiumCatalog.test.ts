import { describe, expect, it } from 'vitest';
import type { AmneziaPremiumCountry, AmneziaPremiumIssuedConfig } from '$lib/types';
import {
	findPremiumCountryTunnel,
	isPremiumCountryAvailable,
	isPremiumCountryIssued,
	isPremiumIssuedConfigActiveDevice,
	isPremiumIssuedConfigReissuable,
	premiumActiveDevicesForCountry,
	premiumCountryConfigFreshness,
	premiumIssuedConfigFreshness,
	premiumIssuedConfigSourceType,
	premiumIssuedConfigsForCountry,
} from './amneziaPremiumCatalog';

// Фикстура НАШЕЙ формы ответа (camelCase), той самой, что отдаёт
// GET /api/amnezia/premium/catalog. Портальный snake_case до фронта не
// доезжает: прочитай хелпер имена по-старому — и sourceType станет пустым
// (все записи переиздаваемы, активное устройство исчезнет как класс), а обе
// отметки времени — undefined (устаревания не будет никогда).
//
// Отметки РАЗНЫЕ и портальная позже выданной: на одинаковых отметках тест
// перестал бы замечать перестановку сравниваемых полей.
function issuedConfig(patch: Partial<AmneziaPremiumIssuedConfig> = {}): AmneziaPremiumIssuedConfig {
	return {
		countryCode: 'de',
		portalUpdatedAt: '2026-05-30T10:00:00Z',
		lastIssuedAt: '2026-05-30T09:00:00Z',
		...patch,
	};
}

function country(patch: Partial<AmneziaPremiumCountry> = {}): AmneziaPremiumCountry {
	return { code: 'is', name: 'Iceland', protocols: ['awg', 'vless'], ...patch };
}

describe('доступность страны по протоколам', () => {
	it('оставляет страну, которую подписка отдаёт по awg', () => {
		expect(isPremiumCountryAvailable(country({ protocols: ['vless', 'awg'] }))).toBe(true);
	});

	it('регистр и пробелы в имени протокола не мешают', () => {
		expect(isPremiumCountryAvailable(country({ protocols: [' AWG ', 'openvpn'] }))).toBe(true);
	});

	it('страну без awg отбрасывает', () => {
		expect(isPremiumCountryAvailable(country({ protocols: ['vless', 'openvpn'] }))).toBe(false);
	});

	// Ядро различия: [] — портал сказал «ничем не отдаётся» (отказ),
	// null/отсутствие — портал про протоколы промолчал (старый ответ CP).
	// Схлопни одно в другое, и страны старого ответа исчезнут из мастера.
	it('пустой список протоколов и отсутствие поля дают РАЗНЫЙ ответ', () => {
		expect(isPremiumCountryAvailable(country({ protocols: [] }))).toBe(false);
		expect(isPremiumCountryAvailable(country({ protocols: null }))).toBe(true);
		expect(isPremiumCountryAvailable(country({ protocols: undefined }))).toBe(true);
	});
});

describe('сопоставление страны с туннелем', () => {
	const tunnels = [
		{ id: 'tun-7', amneziaCountry: ' NL ' },
		{ id: 'tun-4', amneziaCountry: 'ch' },
		{ id: 'tun-9' },
	];

	it('находит туннель страны, не спотыкаясь о регистр и пробелы', () => {
		expect(findPremiumCountryTunnel(tunnels, 'nl')?.id).toBe('tun-7');
		expect(findPremiumCountryTunnel(tunnels, ' Nl')?.id).toBe('tun-7');
		expect(findPremiumCountryTunnel(tunnels, 'CH')?.id).toBe('tun-4');
	});

	it('не выдумывает туннель для страны, которой нет', () => {
		expect(findPremiumCountryTunnel(tunnels, 'is')).toBeUndefined();
	});

	// Иначе страна без кода «соответствовала» бы туннелю без amneziaCountry.
	it('пустой код страны не совпадает с туннелем без страны', () => {
		expect(findPremiumCountryTunnel(tunnels, '   ')).toBeUndefined();
	});
});

describe('устаревание выданной конфигурации', () => {
	// Портал обновил позже, чем мы скачали, — именно в эту сторону.
	// Перестановка отметок местами обязана уронить обе проверки.
	it('считает устаревшей выдачу, которую портал обновил после скачивания', () => {
		const ic = issuedConfig({
			portalUpdatedAt: '2026-06-04T10:00:00Z',
			lastIssuedAt: '2026-06-03T10:00:00Z',
		});
		expect(premiumIssuedConfigFreshness(ic)).toBe('stale');
	});

	it('не считает устаревшей выдачу, скачанную после обновления портала', () => {
		const ic = issuedConfig({
			portalUpdatedAt: '2026-06-03T10:00:00Z',
			lastIssuedAt: '2026-06-04T10:00:00Z',
		});
		expect(premiumIssuedConfigFreshness(ic)).toBe('fresh');
	});

	// «Сравнивать нечем» — не «не устарела»: вернув в обоих случаях один
	// ответ, мастер молча спрячет метку там, где данных не хватает.
	it('различает «сравнивать нечем» и «не устарела»', () => {
		const fresh = issuedConfig({
			portalUpdatedAt: '2026-06-01T10:00:00Z',
			lastIssuedAt: '2026-06-02T10:00:00Z',
		});
		expect(premiumIssuedConfigFreshness(fresh)).toBe('fresh');

		expect(premiumIssuedConfigFreshness(issuedConfig({ portalUpdatedAt: undefined }))).toBe(
			'unknown'
		);
		expect(premiumIssuedConfigFreshness(issuedConfig({ lastIssuedAt: undefined }))).toBe('unknown');
		expect(premiumIssuedConfigFreshness(issuedConfig({ portalUpdatedAt: '   ' }))).toBe('unknown');
		expect(premiumIssuedConfigFreshness(issuedConfig({ lastIssuedAt: 'позавчера' }))).toBe(
			'unknown'
		);
	});

	it('по стране устаревшая запись важнее непонятной, а непонятная — свежей', () => {
		const stale = issuedConfig({
			countryCode: 'nl',
			portalUpdatedAt: '2026-06-08T10:00:00Z',
			lastIssuedAt: '2026-06-07T10:00:00Z',
		});
		const unknown = issuedConfig({ countryCode: 'nl', portalUpdatedAt: 'вчера' });
		const fresh = issuedConfig({
			countryCode: 'nl',
			portalUpdatedAt: '2026-06-05T10:00:00Z',
			lastIssuedAt: '2026-06-06T10:00:00Z',
		});

		expect(premiumCountryConfigFreshness([stale, unknown, fresh], 'NL')).toBe('stale');
		expect(premiumCountryConfigFreshness([unknown, fresh], 'nl')).toBe('unknown');
		expect(premiumCountryConfigFreshness([fresh], 'nl')).toBe('fresh');
	});

	it('страна без выданных конфигураций — «сравнивать нечем», а не «свежая»', () => {
		expect(premiumCountryConfigFreshness([], 'nl')).toBe('unknown');
		expect(premiumCountryConfigFreshness([issuedConfig({ countryCode: 'ch' })], 'nl')).toBe(
			'unknown'
		);
	});
});

describe('активное устройство против переиздаваемой записи', () => {
	it('не считает gateway_account выданной конфигурацией страны', () => {
		const issued = [issuedConfig({ sourceType: 'gateway_account' })];

		expect(isPremiumIssuedConfigReissuable(issued[0])).toBe(false);
		expect(premiumIssuedConfigsForCountry(issued, 'de')).toEqual([]);
		expect(isPremiumCountryIssued(issued, 'de')).toBe(false);
		expect(premiumCountryConfigFreshness(issued, 'de')).toBe('unknown');
	});

	it('приводит sourceType к сравнимому виду до фильтрации', () => {
		const issued = [issuedConfig({ sourceType: ' Gateway_Account ' })];

		expect(isPremiumIssuedConfigReissuable(issued[0])).toBe(false);
		expect(isPremiumCountryIssued(issued, 'DE')).toBe(false);
		expect(premiumCountryConfigFreshness(issued, 'DE')).toBe('unknown');
	});

	it('оставляет запись без sourceType переиздаваемой', () => {
		const issued = [issuedConfig({ sourceType: undefined })];

		expect(isPremiumIssuedConfigReissuable(issued[0])).toBe(true);
		expect(premiumIssuedConfigsForCountry(issued, 'de')).toHaveLength(1);
		expect(isPremiumCountryIssued(issued, 'de')).toBe(true);
		expect(premiumCountryConfigFreshness(issued, 'de')).toBe('stale');
	});

	it('оставляет не-gateway запись переиздаваемой', () => {
		const issued = [issuedConfig({ sourceType: 'downloaded_config' })];

		expect(isPremiumIssuedConfigReissuable(issued[0])).toBe(true);
		expect(isPremiumCountryIssued(issued, 'de')).toBe(true);
		expect(premiumCountryConfigFreshness(issued, 'de')).toBe('stale');
	});

	it('считает страну выданной только по настоящей записи конфигурации', () => {
		const issued = [
			issuedConfig({ sourceType: 'gateway_account' }),
			issuedConfig({
				sourceType: 'downloaded_config',
				lastIssuedAt: '2026-05-30T11:00:00Z',
			}),
		];

		expect(premiumIssuedConfigsForCountry(issued, 'de')).toHaveLength(1);
		expect(isPremiumCountryIssued(issued, 'de')).toBe(true);
		expect(premiumCountryConfigFreshness(issued, 'de')).toBe('fresh');
	});

	it('возвращает активные устройства отдельно, не подмешивая их к выдачам', () => {
		const issued = [
			issuedConfig({ sourceType: 'gateway_account' }),
			issuedConfig({ sourceType: 'downloaded_config' }),
			issuedConfig({ sourceType: ' gateway_account ', countryCode: 'nl' }),
		];

		expect(premiumActiveDevicesForCountry(issued, 'de')).toHaveLength(1);
		expect(premiumIssuedConfigsForCountry(issued, 'de')).toHaveLength(1);
		expect(premiumActiveDevicesForCountry(issued, 'nl')).toHaveLength(1);
	});

	it('приводит sourceType к сравнимому виду для опознания активного устройства', () => {
		const active = issuedConfig({ sourceType: ' Gateway_Account ' });
		const config = issuedConfig({ sourceType: 'downloaded_config' });

		expect(premiumIssuedConfigSourceType(active)).toBe('gateway_account');
		expect(isPremiumIssuedConfigActiveDevice(active)).toBe(true);
		expect(isPremiumIssuedConfigActiveDevice(config)).toBe(false);
	});

	it('оставляет страну с одним активным устройством доступной для выдачи', () => {
		const issued = [issuedConfig({ sourceType: 'gateway_account' })];

		expect(isPremiumCountryIssued(issued, 'de')).toBe(false);
		expect(premiumCountryConfigFreshness(issued, 'de')).toBe('unknown');
		expect(premiumActiveDevicesForCountry(issued, 'de')).toHaveLength(1);
	});

	// Страж формы данных. Записи различаются ТОЛЬКО полями нашего ответа, и
	// портальных имён в них нет вовсе: прочитай хелпер snake_case — и обе
	// записи станут неотличимы, активное устройство уедет в переиздаваемые, а
	// устаревшая выдача перестанет считаться устаревшей.
	it('различает активное устройство и переиздаваемую запись на нашей форме данных', () => {
		const activeDevice: AmneziaPremiumIssuedConfig = {
			countryCode: 'nl',
			sourceType: 'gateway_account',
			portalUpdatedAt: '2026-06-02T10:00:00Z',
			lastIssuedAt: '2026-06-01T10:00:00Z',
		};
		const reissuable: AmneziaPremiumIssuedConfig = {
			countryCode: 'nl',
			sourceType: 'downloaded_config',
			portalUpdatedAt: '2026-06-04T10:00:00Z',
			lastIssuedAt: '2026-06-03T10:00:00Z',
		};
		const issued = [activeDevice, reissuable];

		expect(isPremiumIssuedConfigActiveDevice(activeDevice)).toBe(true);
		expect(isPremiumIssuedConfigActiveDevice(reissuable)).toBe(false);
		expect(premiumActiveDevicesForCountry(issued, 'nl')).toEqual([activeDevice]);
		expect(premiumIssuedConfigsForCountry(issued, 'nl')).toEqual([reissuable]);
		expect(isPremiumCountryIssued(issued, 'nl')).toBe(true);
		expect(premiumCountryConfigFreshness(issued, 'nl')).toBe('stale');
	});
});
