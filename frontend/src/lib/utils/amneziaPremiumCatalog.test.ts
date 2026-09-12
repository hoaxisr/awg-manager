import { describe, expect, it } from 'vitest';
import type { AmneziaPremiumCountry, AmneziaPremiumIssuedConfig } from '$lib/types';
import {
	findPremiumCountryTunnel,
	formatPremiumDate,
	isPremiumCountryAvailable,
	isPremiumCountryIssued,
	isPremiumIssueAllowed,
	isPremiumIssuedConfigActiveDevice,
	isPremiumIssuedConfigReissuable,
	premiumActiveDevicesForCountry,
	premiumCountryConfigFreshness,
	premiumCountryFlag,
	premiumCountryLabel,
	premiumIssuedConfigFreshness,
	premiumIssuedConfigsForCountry,
	premiumIssuedConfigSourceType,
	premiumSubscriptionDaysLeft,
	premiumSubscriptionState,
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

describe('флаг страны из кода', () => {
	it('собирает флаг из пары региональных индикаторов', () => {
		expect(premiumCountryFlag('de')).toBe('\u{1F1E9}\u{1F1EA}');
		expect(premiumCountryFlag('is')).toBe('\u{1F1EE}\u{1F1F8}');
	});

	it('не спотыкается о регистр и пробелы кода', () => {
		expect(premiumCountryFlag('Nl')).toBe('\u{1F1F3}\u{1F1F1}');
		expect(premiumCountryFlag(' CH ')).toBe('\u{1F1E8}\u{1F1ED}');
	});

	// Половина флага и «квадратик с кодом» читаются как поломка вёрстки, а не
	// как отсутствие данных: на любом не-двухбуквенном коде значка просто нет.
	it('на коде не из двух латинских букв отдаёт пустую строку', () => {
		expect(premiumCountryFlag('')).toBe('');
		expect(premiumCountryFlag('d')).toBe('');
		expect(premiumCountryFlag('deu')).toBe('');
		expect(premiumCountryFlag('74')).toBe('');
		expect(premiumCountryFlag('шв')).toBe('');
	});
});

describe('состояние подписки по дате окончания', () => {
	// Часы машины в расчёт не входят: «сейчас» приходит аргументом.
	const now = Date.parse('2026-10-07T04:31:00Z');

	it('подписку с запасом больше порога считает действующей', () => {
		expect(premiumSubscriptionState('2027-02-14T21:05:00Z', now)).toBe('active');
	});

	// Порог — 30 дней, как в клиенте Amnezia (apiUtils.h, withinDays = 30).
	// Ровно порог — уже предупреждение, минутой дальше — ещё нет.
	it('ровно на пороге предупреждает, а минутой дальше — нет', () => {
		expect(premiumSubscriptionState('2026-11-06T04:31:00Z', now)).toBe('expiring');
		expect(premiumSubscriptionState('2026-11-06T04:32:00Z', now)).toBe('active');
	});

	it('считает истёкшей подписку ровно в момент окончания и после него', () => {
		expect(premiumSubscriptionState('2026-10-07T04:31:00Z', now)).toBe('expired');
		expect(premiumSubscriptionState('2026-09-29T18:12:00Z', now)).toBe('expired');
	});

	// «Срока нет» — не «действует» и не «истекла»: и то и другое было бы
	// выдачей неизвестного за известное, каждое в свою сторону.
	it('различает отсутствие даты и мусор от действующей подписки', () => {
		expect(premiumSubscriptionState(undefined, now)).toBe('unknown');
		expect(premiumSubscriptionState('', now)).toBe('unknown');
		expect(premiumSubscriptionState('   ', now)).toBe('unknown');
		expect(premiumSubscriptionState('бессрочно', now)).toBe('unknown');
	});

	it('считает дни до конца вверх: начатый день ещё идёт', () => {
		expect(premiumSubscriptionDaysLeft('2026-10-09T07:48:00Z', now)).toBe(3);
		expect(premiumSubscriptionDaysLeft('2026-11-06T04:31:00Z', now)).toBe(30);
		expect(premiumSubscriptionDaysLeft('2026-11-06T04:32:00Z', now)).toBe(31);
	});

	it('у истёкшей подписки дней не остаётся, а без даты считать нечего', () => {
		expect(premiumSubscriptionDaysLeft('2026-10-07T04:31:00Z', now)).toBe(0);
		expect(premiumSubscriptionDaysLeft('2026-09-29T18:12:00Z', now)).toBe(-7);
		expect(premiumSubscriptionDaysLeft('бессрочно', now)).toBeNull();
		expect(premiumSubscriptionDaysLeft(undefined, now)).toBeNull();
	});
});

describe('доступность выдачи конфигурации', () => {
	// Жёлтая карточка — предупреждение, а не запрет, а отсутствие даты не
	// должно выключать фичу. Реализация «доступна = state === active» обязана
	// уронить обе эти проверки.
	it('истекающая подписка и неизвестный срок выдачу НЕ запрещают', () => {
		expect(isPremiumIssueAllowed('expiring')).toBe(true);
		expect(isPremiumIssueAllowed('unknown')).toBe(true);
	});

	it('действующая подписка выдачу разрешает, истёкшая — запрещает', () => {
		expect(isPremiumIssueAllowed('active')).toBe(true);
		expect(isPremiumIssueAllowed('expired')).toBe(false);
	});
});

describe('форматирование даты подписки', () => {
	// Без года «действует до 08.03» ничего не сообщает: подписки живут годами.
	// Отметки местного времени (без Z) — чтобы проверка не зависела от TZ.
	it('печатает дату с годом', () => {
		expect(formatPremiumDate('2027-03-08T12:00:00')).toBe('08.03.2027');
		expect(formatPremiumDate('2026-11-21T19:47:00')).toBe('21.11.2026');
	});

	// formatDate из utils/format.ts отдала бы здесь '—', то есть нарисовала бы
	// строку «действует до —» там, где строки быть не должно вовсе.
	it('на мусоре и на отсутствии даёт ПУСТУЮ строку, не прочерк и не Invalid Date', () => {
		expect(formatPremiumDate('бессрочно')).toBe('');
		expect(formatPremiumDate('')).toBe('');
		expect(formatPremiumDate('   ')).toBe('');
		expect(formatPremiumDate(undefined)).toBe('');
	});
});

describe('метка строки страны', () => {
	const tunnels = [
		{ id: 'tun-3', name: 'awg-nl-7', amneziaCountry: 'nl' },
		{ id: 'tun-8', name: 'awg-ch-2', amneziaCountry: ' CH ' },
	];

	const staleNL = issuedConfig({
		countryCode: 'nl',
		portalUpdatedAt: '2026-07-19T14:26:00Z',
		lastIssuedAt: '2026-07-11T08:03:00Z',
	});
	const freshNL = issuedConfig({
		countryCode: 'nl',
		portalUpdatedAt: '2026-06-13T22:41:00Z',
		lastIssuedAt: '2026-06-27T05:19:00Z',
	});
	const freshIS = issuedConfig({
		countryCode: 'is',
		portalUpdatedAt: '2026-08-02T11:34:00Z',
		lastIssuedAt: '2026-08-16T03:57:00Z',
	});
	const staleIS = issuedConfig({
		countryCode: 'is',
		portalUpdatedAt: '2026-08-29T09:12:00Z',
		lastIssuedAt: '2026-08-16T03:57:00Z',
	});

	// Ядро контракта: у страны с туннелем конфигурация устаревает точно так же,
	// и лесенка «сначала туннель» спрячет «конфиг устарел» навсегда — ровно у
	// тех, кто пришёл его обновлять.
	it('«конфиг устарел» важнее «туннель», когда есть и то и другое', () => {
		expect(premiumCountryLabel('nl', [staleNL], tunnels)).toEqual({
			kind: 'stale',
			text: 'конфиг устарел',
		});
	});

	it('показывает туннель, когда выданная конфигурация не устарела', () => {
		expect(premiumCountryLabel('nl', [freshNL], tunnels)).toEqual({
			kind: 'tunnel',
			text: 'туннель awg-nl-7',
		});
	});

	// Отдельная ветка: реализация «выдавалась ? устарела : ничего» отдаст здесь
	// «конфиг устарел» и покраснеет.
	it('«уже выдавался» отличается от «устарел» и от «ничего»', () => {
		expect(premiumCountryLabel('is', [freshIS], tunnels)).toEqual({
			kind: 'issued',
			text: 'конфиг уже выдавался',
		});
		expect(premiumCountryLabel('is', [staleIS], tunnels)).toEqual({
			kind: 'stale',
			text: 'конфиг устарел',
		});
		expect(premiumCountryLabel('is', [], tunnels)).toBeNull();
	});

	// Активное устройство — не выданная конфигурация: иначе у страны, где
	// просто живёт устройство подписки, появится метка про конфигурацию,
	// которой никто не выдавал. Но и молчать нельзя (F284): страна уже
	// занимает слот, и без метки пользователь выдаёт по ней вторую запись.
	it('запись активного устройства даёт свою метку, а не «уже выдавался»', () => {
		const active = issuedConfig({ countryCode: 'is', sourceType: 'gateway_account' });
		expect(premiumCountryLabel('is', [active], tunnels)).toEqual({
			kind: 'external',
			text: 'получено вне AWG-M',
		});
	});

	// Приоритет: «получено вне AWG-M» — самая слабая метка. Любая из трёх
	// предыдущих говорит то же самое конкретнее и обязана перебивать её.
	it('«получено вне AWG-M» перебивается выдачей, туннелем и устареванием', () => {
		const device = issuedConfig({ countryCode: 'is', sourceType: 'gateway_account' });

		// Выданная конфигурация той же страны важнее.
		expect(premiumCountryLabel('is', [device, freshIS], tunnels)?.kind).toBe('issued');
		// Устаревшая — тем более.
		expect(premiumCountryLabel('is', [device, staleIS], tunnels)?.kind).toBe('stale');

		// Туннель важнее устройства: страна «ch» в фикстуре туннелей есть.
		const deviceCH = issuedConfig({ countryCode: 'ch', sourceType: 'gateway_account' });
		expect(premiumCountryLabel('ch', [deviceCH], tunnels)).toEqual({
			kind: 'tunnel',
			text: 'туннель awg-ch-2',
		});
	});

	// Метка ищется по СВОЕЙ стране: устройство в другой стране на эту не
	// распространяется, иначе одно устройство пометило бы весь список.
	it('устройство чужой страны метку не ставит', () => {
		const device = issuedConfig({ countryCode: 'is', sourceType: 'gateway_account' });
		expect(premiumCountryLabel('de', [device], tunnels)).toBeNull();
	});

	it('регистр кода страны на метку не влияет', () => {
		expect(premiumCountryLabel('NL', [staleNL], tunnels)?.kind).toBe('stale');
		expect(premiumCountryLabel(' Ch ', [], tunnels)).toEqual({
			kind: 'tunnel',
			text: 'туннель awg-ch-2',
		});
	});

	it('у страны без туннеля и без выдач метки нет', () => {
		expect(premiumCountryLabel('de', [staleNL, freshIS], tunnels)).toBeNull();
	});
});
