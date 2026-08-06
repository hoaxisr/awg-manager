import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import EngineExclusionsCard from './EngineExclusionsCard.svelte';
import { singboxRouter } from '$lib/stores/singboxRouter';
import type { SingboxRouterSettings, SingboxRouterStatus } from '$lib/types';
import type { EngineRoutingMode } from './engineRunState';

const { getSettings, putSettings } = vi.hoisted(() => ({
	getSettings: vi.fn(async () => ({}) as SingboxRouterSettings),
	putSettings: vi.fn(async (_next: SingboxRouterSettings) => ({})),
}));
vi.mock('$lib/api/client', () => ({
	api: {
		singboxRouterGetSettings: getSettings,
		singboxRouterPutSettings: putSettings,
		singboxRouterStatus: vi.fn(async () => ({ enabled: true, active: true })),
	},
	ApiGatewayError: class ApiGatewayError extends Error {},
}));

const MODES: EngineRoutingMode[] = ['tproxy', 'policy-tun', 'fakeip-tun'];

function setMode(routingMode: string, patch: Partial<SingboxRouterSettings> = {}): void {
	singboxRouter.setSettings({
		routingMode,
		bypassPresets: [],
		bypassExtraPorts: '',
		bypassExtraSubnets: '',
		...patch,
	} as SingboxRouterSettings);
}

/** Состояние ядра из статуса: оно обнуляет классы QoS раньше самих классов. */
function setStatus(patch: Partial<SingboxRouterStatus> = {}): void {
	singboxRouter.applyStatus({
		enabled: true,
		installed: true,
		active: true,
		netfilterAvailable: true,
		tproxyTargetAvailable: true,
		policyName: 'SBRouter',
		policyExists: true,
		deviceMode: 'policy',
		snifferEnabled: false,
		deviceCount: 0,
		ruleCount: 0,
		ruleSetCount: 0,
		outboundAwgCount: 0,
		outboundCompositeCount: 0,
		final: 'direct',
		xtDscpAvailable: true,
		...patch,
	});
}

/** Подпись netfilter-блока — пустая строка, если её нет. */
function netfilterNotice(container: HTMLElement): string {
	const sec = [...container.querySelectorAll('.sec')].find((s) =>
		s.querySelector('.sec-cap')?.textContent?.includes('Прямо в WAN'),
	);
	return (sec?.querySelector('.hint.warn, .sec-cap + .hint')?.textContent ?? '').trim();
}

describe('EngineExclusionsCard', () => {
	beforeEach(() => {
		getSettings.mockClear().mockResolvedValue({} as SingboxRouterSettings);
		putSettings.mockClear();
		setStatus();
		setMode('tproxy');
	});

	// ── Карточку не прячем ни в одном режиме ─────────────────────────────────
	//
	// Пресет keendns — DNS-перезапись, а не netfilter: он работает во всех трёх
	// режимах. «Спрятать карточку в FakeIP» унесло бы вместе с враньём
	// работающую настройку.
	it.each(MODES)('в режиме %s пресет KeenDNS остаётся на месте', (mode) => {
		setMode(mode);
		const { getByText } = render(EngineExclusionsCard);
		expect(getByText('KeenDNS / CrazeDNS')).toBeTruthy();
		expect(getByText(/Работает во всех режимах захвата/)).toBeTruthy();
	});

	it.each(MODES)('в режиме %s netfilter-исключения остаются доступны для правки', (mode) => {
		setMode(mode);
		const { getByText, getByLabelText } = render(EngineExclusionsCard);
		for (const preset of ['L2TP / IPsec VPN', 'NTP (синхронизация времени)', 'NetBIOS / SMB']) {
			expect(getByText(preset)).toBeTruthy();
		}
		expect(getByLabelText('Доп. порты')).toBeTruthy();
		expect(getByLabelText('Доп. подсети')).toBeTruthy();
	});

	// KeenDNS не должен уехать в netfilter-блок: там на него повесилась бы
	// чужая подпись «в этом режиме не применяется».
	it('KeenDNS лежит в блоке DNS-перезаписи, а не среди портовых пресетов', () => {
		const { container } = render(EngineExclusionsCard);
		// Именно чипы, а не текст секции: в пояснении под DNS-блоком слово
		// «KeenDNS» есть и так, и проверка по textContent прошла бы даже когда
		// сам чип уехал в netfilter-блок.
		const chips = (caption: string): string[] => {
			const sec = [...container.querySelectorAll('.sec')].find((s) =>
				s.querySelector('.sec-cap')?.textContent?.includes(caption),
			);
			expect(sec).toBeTruthy();
			return [...(sec?.querySelectorAll('.chip') ?? [])].map((c) => c.textContent ?? '');
		};
		const dns = chips('Перезапись DNS');
		const netfilter = chips('Прямо в WAN (netfilter)');
		expect(dns).toHaveLength(1);
		expect(dns[0]).toContain('KeenDNS');
		expect(netfilter).toHaveLength(3);
		expect(netfilter.some((t) => t.includes('KeenDNS'))).toBe(false);
	});

	// ── Честность подписи по режимам ─────────────────────────────────────────
	//
	// Ассерт по всем трём режимам сразу: гейт, написанный как `!policyTunMode`
	// (условие шторки), в FakeIP дал бы «всё работает» — ровно то враньё,
	// которое эта карточка несла молча.
	it.each([
		['tproxy', 0, false],
		['tproxy', 2, false],
		['policy-tun', 0, true],
		['policy-tun', 2, true],
		['fakeip-tun', 0, true],
		['fakeip-tun', 2, true],
	] as const)(
		'%s с %i классами QoS: подпись у netfilter-блока есть=%s',
		(mode, classes, hasNotice) => {
			setMode(mode, {
				qosClasses: Array.from({ length: classes }, (_, i) => ({
					dscp: 10 + i,
					name: `c${i}`,
					outbound: 'direct',
					enabled: true,
				})),
			});
			const { container } = render(EngineExclusionsCard);
			expect(netfilterNotice(container).length > 0).toBe(hasNotice);
		},
	);

	// ── Состояние ядра врёт так же, как врал режим ───────────────────────────
	//
	// Бэкенд обнуляет классы QoS при недоступном netfilter и непригодном
	// xt_dscp РАНЬШЕ, чем смотрит на сами классы (policytun_enable.go:337-350).
	// Считать одни классы — оставить ложь про модуль вместо лжи про режим.
	it('в policy-tun с классом QoS, но без xt_dscp, карточка не обещает работу', () => {
		setStatus({ xtDscpAvailable: false });
		setMode('policy-tun', {
			qosClasses: [{ dscp: 10, name: 'games', outbound: 'direct', enabled: true }],
		});
		const { container } = render(EngineExclusionsCard);
		expect(netfilterNotice(container)).toMatch(/xt_dscp/);
		expect(container.querySelector('.hint.warn')).not.toBeNull();
	});

	it('в policy-tun с классом QoS, но без netfilter, карточка не обещает работу', () => {
		setStatus({ netfilterAvailable: false });
		setMode('policy-tun', {
			qosClasses: [{ dscp: 10, name: 'games', outbound: 'direct', enabled: true }],
		});
		const { container } = render(EngineExclusionsCard);
		expect(netfilterNotice(container)).toMatch(/netfilter на роутере недоступен/);
		expect(container.querySelector('.hint.warn')).not.toBeNull();
	});

	// Легаси-статус без поля: «не сказали» ≠ «сломано».
	it('легаси-статус без xtDscpAvailable не выдаётся за поломку', () => {
		setStatus({ xtDscpAvailable: undefined });
		setMode('policy-tun', {
			qosClasses: [{ dscp: 10, name: 'games', outbound: 'direct', enabled: true }],
		});
		const { container } = render(EngineExclusionsCard);
		expect(netfilterNotice(container)).toMatch(/только внутри цепочки/);
		expect(container.querySelector('.hint.warn')).toBeNull();
	});

	it('в tproxy недоступный xt_dscp на подпись не влияет', () => {
		setStatus({ xtDscpAvailable: false });
		setMode('tproxy');
		const { container } = render(EngineExclusionsCard);
		expect(netfilterNotice(container)).toBe('');
	});

	it('в FakeIP подпись предупреждающая и говорит, что настройка не применяется', () => {
		setMode('fakeip-tun');
		const { getByText, container } = render(EngineExclusionsCard);
		expect(getByText(/В режиме FakeIP эти исключения не применяются/)).toBeTruthy();
		expect(container.querySelector('.hint.warn')).not.toBeNull();
	});

	// Без классов QoS бэкенд в policy-tun не ставит netfilter вообще — это
	// предупреждение. С классами настройка работает, но только на DSCP-меченом
	// трафике: это оговорка, а не тревога.
	it('в policy-tun без классов QoS подпись предупреждающая', () => {
		setMode('policy-tun', { qosClasses: [] });
		const { getByText, container } = render(EngineExclusionsCard);
		expect(getByText(/ни на что не влияют/)).toBeTruthy();
		expect(container.querySelector('.hint.warn')).not.toBeNull();
	});

	it('в policy-tun с классами QoS подпись обычная, без тревоги', () => {
		setMode('policy-tun', {
			qosClasses: [{ dscp: 10, name: 'games', outbound: 'direct', enabled: true }],
		});
		const { getByText, container } = render(EngineExclusionsCard);
		expect(getByText(/только внутри цепочки/)).toBeTruthy();
		expect(container.querySelector('.hint.warn')).toBeNull();
	});

	// Выключенный класс не попадает в qosSpecs бэкенда, значит netfilter-цепочку
	// из-за него не поставят — считать его «настроенным» было бы враньём.
	it('выключенные классы QoS не считаются настроенными', () => {
		setMode('policy-tun', {
			qosClasses: [{ dscp: 10, name: 'games', outbound: 'direct', enabled: false }],
		});
		const { getByText } = render(EngineExclusionsCard);
		expect(getByText(/ни на что не влияют/)).toBeTruthy();
	});

	// ── Сохранение ───────────────────────────────────────────────────────────

	it('клик по пресету сохраняет его в настройки', async () => {
		const { getByText } = render(EngineExclusionsCard);
		await fireEvent.click(getByText('NTP (синхронизация времени)'));
		await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
		expect(putSettings.mock.calls[0][0]).toMatchObject({ bypassPresets: ['ntp'] });
	});

	it('повторный клик по активному пресету его снимает', async () => {
		setMode('tproxy', { bypassPresets: ['ntp', 'keendns'] });
		const { getByText } = render(EngineExclusionsCard);
		await fireEvent.click(getByText('NTP (синхронизация времени)'));
		await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
		expect(putSettings.mock.calls[0][0]).toMatchObject({ bypassPresets: ['keendns'] });
	});

	it('активность пресета видна в разметке', () => {
		setMode('tproxy', { bypassPresets: ['keendns'] });
		const { getByText } = render(EngineExclusionsCard);
		const chip = getByText('KeenDNS / CrazeDNS').closest('button');
		expect(chip?.getAttribute('aria-pressed')).toBe('true');
		expect(getByText('NetBIOS / SMB').closest('button')?.getAttribute('aria-pressed')).toBe(
			'false',
		);
	});

	it('без загруженных настроек карточка не показывает поля', () => {
		singboxRouter.setSettings(null);
		const { getByText, queryByText } = render(EngineExclusionsCard);
		expect(getByText('Настройки движка ещё не загружены.')).toBeTruthy();
		expect(queryByText('KeenDNS / CrazeDNS')).toBeNull();
	});
});
