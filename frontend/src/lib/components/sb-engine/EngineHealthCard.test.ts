import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, fireEvent, screen } from '@testing-library/svelte';
import EngineHealthCard from './EngineHealthCard.svelte';
import EngineCaptureCard from './EngineCaptureCard.svelte';
import { singboxRouter } from '$lib/stores/singboxRouter';
import type { SingboxRouterSettings, SingboxRouterStatus } from '$lib/types';

const { getSettings, putSettings, wanInterfaces, natPreview } = vi.hoisted(() => ({
	getSettings: vi.fn(async () => ({}) as SingboxRouterSettings),
	putSettings: vi.fn(async (_next: SingboxRouterSettings) => ({})),
	wanInterfaces: vi.fn(async () => [
		{ name: 'ppp0', id: 'PPPoE0', label: 'Letai (PPPoE)', up: true },
		{ name: 'eth3', id: 'ISP', label: '', up: false },
	]),
	natPreview: vi.fn(async () => ({ segments: [] })),
}));
vi.mock('$lib/api/client', () => ({
	api: {
		singboxRouterGetSettings: getSettings,
		singboxRouterPutSettings: putSettings,
		singboxRouterListWANInterfaces: wanInterfaces,
		getPolicyTunNATPreview: natPreview,
		singboxRouterStatus: vi.fn(async () => ({ enabled: true, active: true })),
	},
	ApiGatewayError: class ApiGatewayError extends Error {},
}));

// Dropdown пересчитывает позицию панели через ResizeObserver, которого нет в jsdom.
class ResizeObserverStub {
	observe(): void {}
	disconnect(): void {}
}
vi.stubGlobal('ResizeObserver', ResizeObserverStub);

const UNBOUND = 'Интерфейс OpkgTun0 не разрешён ни в одной политике доступа';

function setEngine(
	routingMode: string,
	statusPatch: Partial<SingboxRouterStatus> = {},
	settingsPatch: Partial<SingboxRouterSettings> = {},
): void {
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
		...statusPatch,
	});
	singboxRouter.setSettings({
		routingMode,
		snifferEnabled: false,
		wanAutoDetect: true,
		...settingsPatch,
	} as SingboxRouterSettings);
}

/** Текст строк зависимостей — по элементам DepRow, а не по тексту карточки. */
function depLabels(container: HTMLElement): string[] {
	return [...container.querySelectorAll('.dep-row .label')].map((el) =>
		(el.textContent ?? '').trim(),
	);
}

function issueTexts(container: HTMLElement): string[] {
	return [...container.querySelectorAll('.issue-row .text')].map((el) =>
		(el.textContent ?? '').trim(),
	);
}

async function openDropdown(triggerText: string): Promise<void> {
	await fireEvent.click(screen.getByText(triggerText));
}

async function pickOption(match: RegExp): Promise<void> {
	const option = screen
		.getAllByRole('option')
		.find((el) => match.test((el.textContent ?? '').trim()));
	if (!option) throw new Error(`option ${match} not found`);
	await fireEvent.click(option);
}

describe('EngineHealthCard', () => {
	beforeEach(() => {
		getSettings.mockClear().mockResolvedValue({} as SingboxRouterSettings);
		putSettings.mockClear();
		wanInterfaces.mockClear();
		setEngine('tproxy');
	});

	// ── Зависимости ──────────────────────────────────────────────────────────

	it('в tproxy показывает netfilter и TPROXY target', () => {
		const { container } = render(EngineHealthCard);
		expect(depLabels(container)).toEqual(['netfilter', 'TPROXY target']);
	});

	it.each(['policy-tun', 'fakeip-tun'])('в %s строки TPROXY target нет', (mode) => {
		setEngine(mode);
		const { container } = render(EngineHealthCard);
		expect(depLabels(container)).toEqual(['netfilter']);
	});

	it('версия sing-box показывается строкой', () => {
		const { container } = render(EngineHealthCard);
		const line = [...container.querySelectorAll('.stat-line')].find((el) =>
			el.querySelector('.stat-label')?.textContent?.includes('Версия sing-box'),
		);
		expect(line).toBeTruthy();
		expect(line?.querySelector('.stat-value')?.textContent?.trim()).toBe('—');
	});

	// ── Дедупликация замечания policy-tun-unbound ────────────────────────────

	it('в policy-tun замечание policy-tun-unbound не задваивается на странице', async () => {
		setEngine('policy-tun', {
			policyTunIface: 'opkgtun0',
			policyTunNdmsName: 'OpkgTun0',
			issues: [{ severity: 'warning', kind: 'policy-tun-unbound', message: UNBOUND }],
		});
		// Обе карточки страницы разом: фильтр стоит в «Здоровье», строку рисует
		// карточка захвата — рядом со своей кнопкой «Политики доступа →».
		render(EngineCaptureCard);
		render(EngineHealthCard);
		expect(screen.getAllByText(UNBOUND)).toHaveLength(1);
	});

	it('в policy-tun строку рисует именно карточка захвата', () => {
		setEngine('policy-tun', {
			policyTunIface: 'opkgtun0',
			policyTunNdmsName: 'OpkgTun0',
			issues: [{ severity: 'warning', kind: 'policy-tun-unbound', message: UNBOUND }],
		});
		const health = render(EngineHealthCard);
		expect(issueTexts(health.container)).toEqual([]);
		const capture = render(EngineCaptureCard);
		expect(issueTexts(capture.container)).toEqual([UNBOUND]);
	});

	it('в tproxy то же замечание «Здоровье» показывает — карточки захвата для него нет', () => {
		setEngine('tproxy', {
			issues: [{ severity: 'warning', kind: 'policy-tun-unbound', message: UNBOUND }],
		});
		const { container } = render(EngineHealthCard);
		expect(issueTexts(container)).toEqual([UNBOUND]);
	});

	it('прочие замечания показываются в любом режиме', () => {
		setEngine('policy-tun', {
			issues: [{ severity: 'error', kind: 'orphan-rule', message: 'Правило без outbound' }],
		});
		const { container } = render(EngineHealthCard);
		expect(issueTexts(container)).toEqual(['Правило без outbound']);
	});

	// ── Блок падений (#456) ──────────────────────────────────────────────────

	it('без падений и паузы блока нет', () => {
		const { container } = render(EngineHealthCard);
		expect(container.querySelector('.crash')).toBeNull();
	});

	it('падения и причина показываются', () => {
		setEngine('tproxy', { crashCount: 3, lastCrashReason: 'OOM-kill' });
		const { container } = render(EngineHealthCard);
		const crash = container.querySelector('.crash');
		expect(crash).not.toBeNull();
		expect(crash?.querySelector('.stat-value')?.textContent?.trim()).toBe('3');
		expect(crash?.querySelector('.crash-reason')?.textContent).toContain('OOM-kill');
	});

	// Текст шторки говорил «кнопка „Перезапустить“ НИЖЕ» — она жила в футере
	// шторки. На странице кнопка в шапке, то есть выше карточки.
	it('пауза авто-перезапуска отправляет за кнопкой в шапку страницы, а не «ниже»', () => {
		setEngine('tproxy', {
			crashCount: 2,
			restartSuppressedUntil: new Date(2026, 0, 2, 9, 30).toISOString(),
		});
		const { container } = render(EngineHealthCard);
		const suppressed = container.querySelector('.crash-suppressed');
		const text = (suppressed?.textContent ?? '').replace(/\s+/g, ' ');
		expect(text).toContain('приостановлен до 09:30');
		expect(text).toContain('в шапке страницы');
		expect(text).not.toMatch(/ниже/);
	});

	it('пауза без падений не показывает счётчик', () => {
		setEngine('tproxy', {
			crashCount: 0,
			restartSuppressedUntil: new Date(2026, 0, 2, 9, 30).toISOString(),
		});
		const { container } = render(EngineHealthCard);
		expect(container.querySelector('.crash')).not.toBeNull();
		expect(container.querySelector('.crash .stat-line')).toBeNull();
	});

	// ── WAN ──────────────────────────────────────────────────────────────────

	it('при включённом авто селекта нет и список интерфейсов не запрашивается', () => {
		const { queryByText } = render(EngineHealthCard);
		expect(queryByText('Интерфейс')).toBeNull();
		expect(wanInterfaces).not.toHaveBeenCalled();
	});

	it('выключение авто показывает селект и подгружает интерфейсы', async () => {
		const { container, getByLabelText } = render(EngineHealthCard);
		await fireEvent.click(getByLabelText('Определять WAN автоматически'));
		await vi.waitFor(() => expect(wanInterfaces).toHaveBeenCalledTimes(1));
		expect(container.textContent).toContain('Интерфейс');
		// Само выключение не персистится: пустой wanInterface бэкенд отклонит.
		expect(putSettings).not.toHaveBeenCalled();
	});

	it('выбор интерфейса сохраняет его вместе с выключенным авто', async () => {
		const { getByLabelText } = render(EngineHealthCard);
		await fireEvent.click(getByLabelText('Определять WAN автоматически'));
		await vi.waitFor(() => expect(wanInterfaces).toHaveBeenCalled());
		await openDropdown('— выберите —');
		await pickOption(/ppp0 — Letai/);
		await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
		expect(putSettings.mock.calls[0][0]).toMatchObject({
			wanAutoDetect: false,
			wanInterface: 'ppp0',
		});
	});

	it('возврат к авто сохраняется и очищает интерфейс', async () => {
		setEngine('tproxy', {}, { wanAutoDetect: false, wanInterface: 'ppp0' });
		const { getByLabelText } = render(EngineHealthCard);
		await fireEvent.click(getByLabelText('Определять WAN автоматически'));
		await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
		expect(putSettings.mock.calls[0][0]).toMatchObject({
			wanAutoDetect: true,
			wanInterface: '',
		});
	});

	// ── Анализ трафика ───────────────────────────────────────────────────────

	it('sniffer сохраняется', async () => {
		const { getByLabelText } = render(EngineHealthCard);
		await fireEvent.click(getByLabelText('Включить sniff'));
		await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
		expect(putSettings.mock.calls[0][0]).toMatchObject({ snifferEnabled: true });
	});

	it('UDP-таймаут сохраняется выбранным значением', async () => {
		render(EngineHealthCard);
		await openDropdown('По умолчанию (5 мин)');
		await pickOption(/^15 минут$/);
		await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
		expect(putSettings.mock.calls[0][0]).toMatchObject({ udpTimeout: '15m0s' });
	});

	it('возврат к «По умолчанию» снимает поле', async () => {
		setEngine('tproxy', {}, { udpTimeout: '15m0s' });
		render(EngineHealthCard);
		await openDropdown('15 минут');
		await pickOption(/^По умолчанию/);
		await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
		expect(putSettings.mock.calls[0][0].udpTimeout).toBeUndefined();
	});

	// ── Настройки видны во всех режимах ──────────────────────────────────────
	//
	// WAN, sniffer и UDP-таймаут применяются всеми тремя генераторами конфига,
	// и «Здоровье» — единственное место их правки после смерти шторки.
	it.each(['tproxy', 'policy-tun', 'fakeip-tun'])('в %s общие настройки на месте', (mode) => {
		setEngine(mode);
		const { getByLabelText, container } = render(EngineHealthCard);
		expect(getByLabelText('Определять WAN автоматически')).toBeTruthy();
		expect(getByLabelText('Включить sniff')).toBeTruthy();
		expect(container.textContent).toContain('UDP-таймаут сессии');
	});

	it('без загруженных настроек полей нет', () => {
		singboxRouter.setSettings(null);
		const { getByText, queryByLabelText } = render(EngineHealthCard);
		expect(getByText('Настройки движка ещё не загружены.')).toBeTruthy();
		expect(queryByLabelText('Включить sniff')).toBeNull();
	});
});
