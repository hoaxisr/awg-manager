import { describe, expect, it } from 'vitest';
import type { AmneziaPremiumIssuedConfig } from '$lib/types';
import {
	isPremiumCountryConfigStale,
	isPremiumCountryIssued,
	isPremiumIssuedConfigActiveDevice,
	isPremiumIssuedConfigReissuable,
	premiumActiveDevicesForCountry,
	premiumIssuedConfigSourceType,
	premiumIssuedConfigsForCountry,
} from './amneziaPremiumVpnPaste';

// Фикстура НАШЕЙ формы ответа (camelCase), той самой, что отдаёт
// GET /api/amnezia/premium/catalog. Портальный snake_case до фронта не
// доезжает: прочитай хелпер имена по-старому — и sourceType станет пустым
// (все записи переиздаваемы, активное устройство исчезнет как класс), а обе
// отметки времени — undefined (устаревания не будет никогда).
function issuedConfig(patch: Partial<AmneziaPremiumIssuedConfig> = {}): AmneziaPremiumIssuedConfig {
	return {
		countryCode: 'de',
		portalUpdatedAt: '2026-05-30T10:00:00Z',
		lastIssuedAt: '2026-05-30T09:00:00Z',
		...patch,
	};
}

describe('amneziaPremiumVpnPaste issued config helpers', () => {
	it('does not treat gateway_account as an issued country config', () => {
		const issued = [issuedConfig({ sourceType: 'gateway_account' })];

		expect(isPremiumIssuedConfigReissuable(issued[0])).toBe(false);
		expect(premiumIssuedConfigsForCountry(issued, 'de')).toEqual([]);
		expect(isPremiumCountryIssued(issued, 'de')).toBe(false);
		expect(isPremiumCountryConfigStale(issued, 'de')).toBe(false);
	});

	it('normalizes gateway_account sourceType before filtering', () => {
		const issued = [issuedConfig({ sourceType: ' Gateway_Account ' })];

		expect(isPremiumIssuedConfigReissuable(issued[0])).toBe(false);
		expect(isPremiumCountryIssued(issued, 'DE')).toBe(false);
		expect(isPremiumCountryConfigStale(issued, 'DE')).toBe(false);
	});

	it('keeps legacy issued configs without sourceType as reissuable', () => {
		const issued = [issuedConfig({ sourceType: undefined })];

		expect(isPremiumIssuedConfigReissuable(issued[0])).toBe(true);
		expect(premiumIssuedConfigsForCountry(issued, 'de')).toHaveLength(1);
		expect(isPremiumCountryIssued(issued, 'de')).toBe(true);
		expect(isPremiumCountryConfigStale(issued, 'de')).toBe(true);
	});

	it('keeps non-gateway issued configs as reissuable', () => {
		const issued = [issuedConfig({ sourceType: 'downloaded_config' })];

		expect(isPremiumIssuedConfigReissuable(issued[0])).toBe(true);
		expect(isPremiumCountryIssued(issued, 'de')).toBe(true);
		expect(isPremiumCountryConfigStale(issued, 'de')).toBe(true);
	});

	it('treats a mixed country as issued only when it has a real config entry', () => {
		const issued = [
			issuedConfig({ sourceType: 'gateway_account' }),
			issuedConfig({
				sourceType: 'downloaded_config',
				lastIssuedAt: '2026-05-30T11:00:00Z',
			}),
		];

		expect(premiumIssuedConfigsForCountry(issued, 'de')).toHaveLength(1);
		expect(isPremiumCountryIssued(issued, 'de')).toBe(true);
		expect(isPremiumCountryConfigStale(issued, 'de')).toBe(false);
	});

	it('returns active devices separately without mixing them into reissuable configs', () => {
		const issued = [
			issuedConfig({ sourceType: 'gateway_account' }),
			issuedConfig({ sourceType: 'downloaded_config' }),
			issuedConfig({ sourceType: ' gateway_account ', countryCode: 'nl' }),
		];

		expect(premiumActiveDevicesForCountry(issued, 'de')).toHaveLength(1);
		expect(premiumIssuedConfigsForCountry(issued, 'de')).toHaveLength(1);
		expect(premiumActiveDevicesForCountry(issued, 'nl')).toHaveLength(1);
	});

	it('normalizes sourceType for active-device detection', () => {
		const active = issuedConfig({ sourceType: ' Gateway_Account ' });
		const config = issuedConfig({ sourceType: 'downloaded_config' });

		expect(premiumIssuedConfigSourceType(active)).toBe('gateway_account');
		expect(isPremiumIssuedConfigActiveDevice(active)).toBe(true);
		expect(isPremiumIssuedConfigActiveDevice(config)).toBe(false);
	});

	it('keeps active-device-only countries available for direct download flow', () => {
		const issued = [issuedConfig({ sourceType: 'gateway_account' })];

		expect(isPremiumCountryIssued(issued, 'de')).toBe(false);
		expect(isPremiumCountryConfigStale(issued, 'de')).toBe(false);
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
		expect(isPremiumCountryConfigStale(issued, 'nl')).toBe(true);
	});
});
