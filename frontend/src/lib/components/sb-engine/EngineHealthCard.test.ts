import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, fireEvent, screen } from '@testing-library/svelte';
import EngineHealthCard from './EngineHealthCard.svelte';
import EngineCaptureCard from './EngineCaptureCard.svelte';
import { singboxRouter } from '$lib/stores/singboxRouter';
import { singboxStatus } from '$lib/stores/singbox';
import { systemInfo } from '$lib/stores/system';
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

function versionValue(container: HTMLElement): string | undefined {
	const line = [...container.querySelectorAll('.stat-line')].find((el) =>
		el.querySelector('.stat-label')?.textContent?.includes('Версия sing-box'),
	);
	expect(line).toBeTruthy();
	return line?.querySelector('.stat-value')?.textContent?.trim();
}

/** Текст блока падений одной строкой — без переносов вёрстки. */
function crashText(container: HTMLElement): string {
	return (container.querySelector('.crash')?.textContent ?? '').replace(/\s+/g, ' ').trim();
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
		wanInterfaces.mockClear().mockResolvedValue([
			{ name: 'ppp0', id: 'PPPoE0', label: 'Letai (PPPoE)', up: true },
			{ name: 'eth3', id: 'ISP', label: '', up: false },
		]);
		// Сторы версии — модульные синглтоны: без сброса значение течёт между тестами.
		singboxStatus.applyMutationResponse(null as never);
		systemInfo.applyMutationResponse(null as never);
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
		expect(versionValue(container)).toBe('—');
	});

	// Цепочка та же, что была в шапке шторки: статус установки sing-box, затем
	// сводка системы. У самого router-статуса поля версии НЕТ
	// (SingboxRouterStatusData, singbox_router_dto.go).
	it('версию берёт из статуса sing-box', () => {
		singboxStatus.applyMutationResponse({ version: '1.13.11' } as never);
		const { container } = render(EngineHealthCard);
		expect(versionValue(container)).toBe('v1.13.11');
	});

	it('без version падает на currentVersion статуса установки', () => {
		singboxStatus.applyMutationResponse({ currentVersion: '1.12.4' } as never);
		const { container } = render(EngineHealthCard);
		expect(versionValue(container)).toBe('v1.12.4');
	});

	it('без статуса sing-box берёт версию из сводки системы', () => {
		systemInfo.applyMutationResponse({ singbox: { version: '1.11.0' } } as never);
		const { container } = render(EngineHealthCard);
		expect(versionValue(container)).toBe('v1.11.0');
	});

	it('статус sing-box приоритетнее сводки системы', () => {
		singboxStatus.applyMutationResponse({ version: '1.13.11' } as never);
		systemInfo.applyMutationResponse({ singbox: { version: '1.11.0' } } as never);
		const { container } = render(EngineHealthCard);
		expect(versionValue(container)).toBe('v1.13.11');
	});

	it('пробельную версию показывает прочерком, а не «v»', () => {
		singboxStatus.applyMutationResponse({ version: '   ' } as never);
		const { container } = render(EngineHealthCard);
		expect(versionValue(container)).toBe('—');
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

	// ── Дубликат engine-dead-interception (I1) ───────────────────────────────
	//
	// Бэкенд вкладывает паузу и счётчик прямо в ТЕКСТ замечания
	// (service_lifecycle.go): «…Автоперезапуск приостановлен до 19:37 (падений
	// за 10 мин: 3).» Блок падений печатал те же два факта своими полями —
	// в шторке их разделяли две секции, на странице они встык.

	const DEAD = 'Движок остановлен, но перехват трафика активен — трафик политик не ходит.';
	const suppressedAt = () => new Date(2026, 0, 2, 19, 37).toISOString();

	function setDeadInterception(statusPatch: Partial<SingboxRouterStatus> = {}): void {
		setEngine('tproxy', {
			issues: [
				{
					severity: 'error',
					kind: 'engine-dead-interception',
					message: `${DEAD} Автоперезапуск приостановлен до 19:37 (падений за 10 мин: 3).`,
				},
			],
			...statusPatch,
		});
	}

	it('замечание + падения: счётчик и пауза не повторяются вторым текстом', () => {
		setDeadInterception({
			crashCount: 3,
			lastCrashReason: 'OOM-kill',
			restartSuppressedUntil: suppressedAt(),
		});
		const { container } = render(EngineHealthCard);
		// Факты сказаны ровно один раз — в тексте замечания.
		expect(issueTexts(container)[0]).toContain('приостановлен до 19:37');
		expect(crashText(container)).not.toContain('Падений за 10 мин');
		expect(crashText(container)).not.toContain('приостановлен до');
		expect(container.querySelector('.crash .stat-line')).toBeNull();
		expect(container.querySelector('.crash-suppressed')).toBeNull();
	});

	// Причины падения в тексте замечания НЕТ — блок обязан её сохранить.
	it('замечание + падения: причина и путь к кнопке остаются', () => {
		setDeadInterception({
			crashCount: 3,
			lastCrashReason: 'OOM-kill',
			restartSuppressedUntil: suppressedAt(),
		});
		const { container } = render(EngineHealthCard);
		expect(crashText(container)).toContain('Причина: OOM-kill');
		expect(crashText(container)).toContain('в шапке страницы');
	});

	// Подсказка про кнопку обязана стоять самостоятельно: «Автоперезапуск
	// приостановлен до X» перед ней больше не печатается.
	it('замечание + падения: подсказка про кнопку — законченное предложение', () => {
		setDeadInterception({ crashCount: 3, restartSuppressedUntil: suppressedAt() });
		const { container } = render(EngineHealthCard);
		const hint = container.querySelector('.crash-hint')?.textContent?.trim() ?? '';
		expect(hint).toMatch(/^Кнопка «Перезапустить»/);
		expect(hint).toMatch(/\.$/);
	});

	// Ветка бэкенда «Автоперезапуск: при следующей проверке (до 30 с)» живёт
	// ТОЛЬКО в тексте замечания: crash-статистики при ней нет, блока тоже.
	it('замечание без падений и паузы: блока нет, факт не потерян', () => {
		setEngine('tproxy', {
			issues: [
				{
					severity: 'error',
					kind: 'engine-dead-interception',
					message: `${DEAD} Автоперезапуск: при следующей проверке (до 30 с).`,
				},
			],
		});
		const { container } = render(EngineHealthCard);
		expect(container.querySelector('.crash')).toBeNull();
		expect(issueTexts(container)[0]).toContain('при следующей проверке');
	});

	// C1. Вторая ветка текста замечания — «Автоперезапуск: при следующей
	// проверке (до 30 с)» — счётчика НЕ содержит. Гейт по одному kind прятал
	// его и здесь: в crash-loop страница показывала одно падение вместо трёх.
	it('замечание без паузы: счётчик падений остаётся на странице', () => {
		setEngine('tproxy', {
			issues: [
				{
					severity: 'error',
					kind: 'engine-dead-interception',
					message: `${DEAD} Автоперезапуск: при следующей проверке (до 30 с).`,
				},
			],
			crashCount: 3,
			lastCrashReason: 'OOM-kill',
		});
		const { container } = render(EngineHealthCard);
		expect(container.querySelector('.crash .stat-value')?.textContent?.trim()).toBe('3');
		expect(crashText(container)).toContain('Причина: OOM-kill');
		// Паузы нет — выдумывать её нельзя.
		expect(crashText(container)).not.toContain('приостановлен');
		// Движок мёртв, значит путь к кнопке уместен и без паузы.
		expect(crashText(container)).toContain('в шапке страницы');
	});

	it('падения без замечания: блок печатает всё сам', () => {
		setEngine('tproxy', {
			crashCount: 3,
			lastCrashReason: 'OOM-kill',
			restartSuppressedUntil: suppressedAt(),
		});
		const { container } = render(EngineHealthCard);
		expect(crashText(container)).toContain('Падений за 10 мин');
		expect(crashText(container)).toContain('приостановлен до 19:37');
		expect(crashText(container)).toContain('Причина: OOM-kill');
		expect(container.querySelector('.crash-hint')).toBeNull();
	});

	it('замечание другого рода блок падений не глушит', () => {
		setEngine('tproxy', {
			issues: [{ severity: 'error', kind: 'orphan-rule', message: 'Правило без outbound' }],
			crashCount: 3,
			restartSuppressedUntil: suppressedAt(),
		});
		const { container } = render(EngineHealthCard);
		expect(crashText(container)).toContain('Падений за 10 мин');
		expect(crashText(container)).toContain('приостановлен до 19:37');
	});

	it('ни замечания, ни падений: секции «Замечания» нет вовсе', () => {
		const { container } = render(EngineHealthCard);
		expect(container.textContent).not.toContain('Замечания');
	});

	// ── Счётчик замечаний (m3, пункт E15 инвентаря) ──────────────────────────

	it('заголовок секции несёт счётчик замечаний', () => {
		setEngine('tproxy', {
			issues: [
				{ severity: 'error', kind: 'orphan-rule', message: 'A' },
				{ severity: 'warning', kind: 'orphan-outbound', message: 'B' },
			],
		});
		const { container } = render(EngineHealthCard);
		expect(container.querySelector('.sec-cap .badge')?.textContent?.trim()).toBe('2');
	});

	// Счётчик — про замечания, а не про блок падений: он честен ровно потому,
	// что дубликат из блока убран.
	it('блок падений без замечаний счётчика не рисует', () => {
		setEngine('tproxy', { crashCount: 3 });
		const { container } = render(EngineHealthCard);
		expect(container.querySelector('.sec-cap .badge')).toBeNull();
	});

	// ── WAN ──────────────────────────────────────────────────────────────────
	//
	// Тумблер «Определять WAN автоматически» + зависимый селект заменены одним
	// списком: пара (wanAutoDetect, wanInterface) у бэкенда жёстко связана, и
	// тумблер выражал её невалидную половину — выключение не сохранялось ВООБЩЕ,
	// а страница показывала закреплённый WAN поверх авто-определения.

	it('WAN — один список, тумблера нет', () => {
		const { getByLabelText, queryByLabelText } = render(EngineHealthCard);
		expect(getByLabelText('Интерфейс')).toBeTruthy();
		expect(queryByLabelText('Определять WAN автоматически')).toBeNull();
	});

	it('список интерфейсов грузится сразу — он нужен всегда', async () => {
		render(EngineHealthCard);
		await vi.waitFor(() => expect(wanInterfaces).toHaveBeenCalledTimes(1));
	});

	it('при авто-определении выбран пункт «Автоматически»', async () => {
		const { getByLabelText } = render(EngineHealthCard);
		await vi.waitFor(() => expect(wanInterfaces).toHaveBeenCalled());
		expect(getByLabelText('Интерфейс').textContent).toContain('Автоматически');
	});

	it('выбор интерфейса сохраняет пару целиком', async () => {
		render(EngineHealthCard);
		await vi.waitFor(() => expect(wanInterfaces).toHaveBeenCalled());
		await openDropdown('Автоматически');
		await pickOption(/ppp0 — Letai/);
		await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
		expect(putSettings.mock.calls[0][0]).toMatchObject({
			wanAutoDetect: false,
			wanInterface: 'ppp0',
		});
	});

	it('возврат к «Автоматически» сохраняется и очищает интерфейс', async () => {
		setEngine('tproxy', {}, { wanAutoDetect: false, wanInterface: 'ppp0' });
		const { getByLabelText } = render(EngineHealthCard);
		await vi.waitFor(() =>
			expect(getByLabelText('Интерфейс').textContent).toContain('Letai'),
		);
		await openDropdown('ppp0 — Letai (PPPoE)');
		await pickOption(/^Автоматически$/);
		await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
		expect(putSettings.mock.calls[0][0]).toMatchObject({
			wanAutoDetect: true,
			wanInterface: '',
		});
	});

	it('повторный выбор текущего пункта ничего не сохраняет', async () => {
		render(EngineHealthCard);
		await vi.waitFor(() => expect(wanInterfaces).toHaveBeenCalled());
		await openDropdown('Автоматически');
		await pickOption(/^Автоматически$/);
		expect(putSettings).not.toHaveBeenCalled();
	});

	// Список не загрузился или интерфейс исчез из системы: закрепление обязано
	// остаться видимым, иначе следующий выбор молча стёр бы его.
	it('сохранённый интерфейс вне списка остаётся выбранным', async () => {
		wanInterfaces.mockResolvedValue([]);
		setEngine('tproxy', {}, { wanAutoDetect: false, wanInterface: 'ppp0' });
		const { getByLabelText } = render(EngineHealthCard);
		await vi.waitFor(() => expect(wanInterfaces).toHaveBeenCalled());
		expect(getByLabelText('Интерфейс').textContent).toContain('ppp0');
	});

	// m5: молчаливый catch делал вид, что ретрай возможен, а эффект больше не
	// перезапускался. Отказ виден и повторяем кнопкой.
	it('отказ загрузки списка виден и повторяется кнопкой', async () => {
		wanInterfaces.mockRejectedValueOnce(new Error('сеть'));
		const { container, getByRole } = render(EngineHealthCard);
		await vi.waitFor(() => expect(container.textContent).toContain('Не удалось загрузить'));
		wanInterfaces.mockResolvedValue([
			{ name: 'ppp0', id: 'PPPoE0', label: 'Letai (PPPoE)', up: true },
		]);
		await fireEvent.click(getByRole('button', { name: 'Повторить' }));
		await vi.waitFor(() => expect(wanInterfaces).toHaveBeenCalledTimes(2));
		await vi.waitFor(() => expect(container.textContent).not.toContain('Не удалось загрузить'));
	});

	// Латч остаётся одноразовым намеренно: сделай повтор автоматическим — и
	// упавшая ручка будет долбиться без остановки. Близнец теста живёт в
	// EngineQosCard.test.ts.
	it('упавший запрос сам себя не перезапускает', async () => {
		wanInterfaces.mockRejectedValue(new Error('сеть'));
		const { container } = render(EngineHealthCard);
		await vi.waitFor(() => expect(container.textContent).toContain('Не удалось загрузить'));
		await new Promise((r) => setTimeout(r, 20));
		expect(wanInterfaces).toHaveBeenCalledTimes(1);
	});

	// I-1. Dropdown коммитил выбор сам (`value` — $bindable, карточка передаёт
	// его без bind:), а провал сохранения стор не трогает. Проп после этого не
	// меняется — значит записанный компонентом пункт остаётся на экране навсегда
	// и врёт про закреплённый WAN ровно так же, как чинили в I3.
	it('провал сохранения WAN не оставляет на экране чужой интерфейс', async () => {
		putSettings.mockRejectedValue(new Error('операция уже выполняется'));
		const { getByLabelText } = render(EngineHealthCard);
		await vi.waitFor(() => expect(wanInterfaces).toHaveBeenCalled());
		await openDropdown('Автоматически');
		await pickOption(/ppp0 — Letai/);
		await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
		expect(getByLabelText('Интерфейс').textContent).toContain('Автоматически');
		expect(getByLabelText('Интерфейс').textContent).not.toContain('Letai');
	});

	// Обратная сторона того же: принятый бэкендом выбор обязан доехать до
	// экрана, иначе список замер бы навсегда.
	it('принятый бэкендом выбор WAN доезжает до списка', async () => {
		const { getByLabelText } = render(EngineHealthCard);
		await vi.waitFor(() => expect(wanInterfaces).toHaveBeenCalled());
		await openDropdown('Автоматически');
		await pickOption(/ppp0 — Letai/);
		await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
		// После удачного PUT настройки перезагружает mergeAndSaveSettings.
		setEngine('tproxy', {}, { wanAutoDetect: false, wanInterface: 'ppp0' });
		await vi.waitFor(() =>
			expect(getByLabelText('Интерфейс').textContent).toContain('Letai'),
		);
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

	// Та же болезнь, что у списка WAN: тумблер коммитил себя сам, а провал
	// сохранения стор не трогает.
	it('провал сохранения sniffer не оставляет тумблер включённым', async () => {
		putSettings.mockRejectedValue(new Error('операция уже выполняется'));
		const { getByLabelText } = render(EngineHealthCard);
		const toggle = getByLabelText('Включить sniff') as HTMLInputElement;
		await fireEvent.click(toggle);
		await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
		expect(toggle.checked).toBe(false);
	});

	it('провал сохранения UDP-таймаута не оставляет чужое значение', async () => {
		putSettings.mockRejectedValue(new Error('операция уже выполняется'));
		const { container } = render(EngineHealthCard);
		await openDropdown('По умолчанию (5 мин)');
		await pickOption(/^15 минут$/);
		await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
		expect(container.querySelector('#eng-udp-timeout')?.textContent ?? '').toContain(
			'По умолчанию',
		);
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
		expect(getByLabelText('Интерфейс')).toBeTruthy();
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
