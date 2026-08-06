import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';
import { render, fireEvent } from '@testing-library/svelte';
import EngineSelectiveCard from './EngineSelectiveCard.svelte';
import { singboxRouter } from '$lib/stores/singboxRouter';
import { selectiveBypass } from '$lib/stores/selectiveBypass';
import { notifications } from '$lib/stores/notifications';
import type {
	SelectiveStatus,
	SingboxRouterSettings,
	SingboxRouterStatus,
} from '$lib/types';
import type { EngineRoutingMode } from './engineRunState';

const { selectiveStatus, installDeps, rebuild, getSettings, putSettings, matchers } = vi.hoisted(
	() => ({
		selectiveStatus: vi.fn(),
		installDeps: vi.fn(),
		rebuild: vi.fn(),
		getSettings: vi.fn(async () => ({}) as SingboxRouterSettings),
		putSettings: vi.fn(async (_next: SingboxRouterSettings) => ({})),
		matchers: vi.fn(async () => ({ matchers: [], total: 0 })),
	}),
);
vi.mock('$lib/api/client', async () => {
	const { ApiGatewayError } = await import('$lib/api/clientCore');
	return {
		api: {
			singboxRouterSelectiveStatus: selectiveStatus,
			singboxRouterSelectiveInstallDeps: installDeps,
			singboxRouterSelectiveRebuild: rebuild,
			singboxRouterSelectiveSnapshotMatchers: matchers,
			singboxRouterGetSettings: getSettings,
			singboxRouterPutSettings: putSettings,
			singboxRouterStatus: vi.fn(async () => ({ enabled: true, active: true })),
		},
		ApiGatewayError,
	};
});

const MODES: EngineRoutingMode[] = ['tproxy', 'policy-tun', 'fakeip-tun'];
const TITLE = 'Селективный перехват';

function sel(patch: Partial<SelectiveStatus> = {}): SelectiveStatus {
	return {
		available: true,
		xtSetAvailable: true,
		conntrackAvailable: true,
		installing: false,
		enabled: true,
		entryCount: 128,
		...patch,
	};
}

function setEngine(routingMode: string, patch: Partial<SingboxRouterStatus> = {}): void {
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
		...patch,
	});
	singboxRouter.setSettings({ routingMode, selectiveBypass: false } as SingboxRouterSettings);
}

function setSelectiveEnabled(on: boolean): void {
	singboxRouter.setSettings({
		routingMode: 'tproxy',
		selectiveBypass: on,
	} as SingboxRouterSettings);
}

describe('EngineSelectiveCard', () => {
	beforeEach(() => {
		selectiveStatus.mockReset().mockResolvedValue(sel());
		installDeps.mockReset().mockResolvedValue(sel());
		rebuild.mockReset().mockResolvedValue(sel({ rebuilding: true }));
		notifications.clearAll();
		selectiveBypass.applyStatus(sel());
		setEngine('tproxy');
	});

	// ── Гейт: только TPROXY ──────────────────────────────────────────────────
	//
	// Ассерт по всем трём режимам сразу. Условие шторки было `!policyTunMode`, и
	// в FakeIP оно даёт false — то есть карточка показала бы чисто
	// TPROXY-механику как живую. Проверка «не видно в policy-tun» одна такой
	// гейт бы пропустила.
	it.each(MODES)('в режиме %s карточка видна ровно тогда, когда это tproxy', (mode) => {
		setEngine(mode);
		const { queryByText } = render(EngineSelectiveCard);
		expect(queryByText(TITLE) !== null).toBe(mode === 'tproxy');
	});

	it('легаси-ответ без routingMode читается как tproxy — карточка есть', () => {
		singboxRouter.setSettings({ selectiveBypass: false } as SingboxRouterSettings);
		const { getByText } = render(EngineSelectiveCard);
		expect(getByText(TITLE)).toBeTruthy();
	});

	// ── Прайминг статуса ─────────────────────────────────────────────────────
	//
	// До nav-v3 статус грузила только шторка sb-router. Её больше нет: не
	// загрузим здесь — карточка пустая навсегда.
	it('в tproxy без загруженного статуса карточка грузит его сама', async () => {
		selectiveBypass.status.set(null);
		render(EngineSelectiveCard);
		await vi.waitFor(() => expect(selectiveStatus).toHaveBeenCalledTimes(1));
	});

	it('уже загруженный статус повторно не запрашивается', async () => {
		render(EngineSelectiveCard);
		await Promise.resolve();
		expect(selectiveStatus).not.toHaveBeenCalled();
	});

	it.each(MODES.filter((m) => m !== 'tproxy'))(
		'в режиме %s статус не запрашивается — карточки там нет',
		async (mode) => {
			selectiveBypass.status.set(null);
			setEngine(mode);
			render(EngineSelectiveCard);
			await Promise.resolve();
			expect(selectiveStatus).not.toHaveBeenCalled();
		},
	);

	// ── Гард route.final ─────────────────────────────────────────────────────

	it('при route.final ≠ direct тумблер заблокирован и объясняет несовместимость', () => {
		setEngine('tproxy', { final: 'proxy' });
		const { container, getByText } = render(EngineSelectiveCard);
		const toggle = container.querySelector('input[type="checkbox"]') as HTMLInputElement;
		expect(toggle.disabled).toBe(true);
		expect(getByText(/route.final = «proxy»/)).toBeTruthy();
		expect(getByText(/Установите final в «direct»/)).toBeTruthy();
	});

	// Дыра, которую `disabled` НЕ закрывает: пока статус роутера не пришёл,
	// routeFinal подставляет «direct», тумблер активен — и клик в этом окне
	// сохранил бы selectiveBypass при реальном final = proxy. Гард, посчитанный
	// из того же выражения, что и `disabled`, до обработчика не доходит вовсе.
	it('пока статус движка не загружен, включение отклоняется, а не сохраняется вслепую', async () => {
		// Стор статуса снаружи read-only; null — ровно то состояние, с которого
		// он стартует, поэтому сбрасываем тем же applyStatus.
		singboxRouter.applyStatus(null as unknown as SingboxRouterStatus);
		const { container } = render(EngineSelectiveCard);
		const toggle = container.querySelector('input[type="checkbox"]') as HTMLInputElement;
		expect(toggle.disabled).toBe(false);
		await fireEvent.click(toggle);
		await vi.waitFor(() =>
			expect(get(notifications).some((n) => /route\.final/.test(n.message))).toBe(true),
		);
		expect(putSettings).not.toHaveBeenCalled();
	});

	// Обратная половина: с загруженным статусом и final = direct клик сохраняет.
	// Без неё гард «отклонять всё подряд» прошёл бы тест выше.
	it('при route.final = direct тумблер доступен', () => {
		const { container } = render(EngineSelectiveCard);
		const toggle = container.querySelector('input[type="checkbox"]') as HTMLInputElement;
		expect(toggle.disabled).toBe(false);
	});

	// ── ipset ────────────────────────────────────────────────────────────────

	it('без пакета ipset тумблер заблокирован и предлагается установка', async () => {
		selectiveBypass.applyStatus(sel({ available: false }));
		const { container, getByText } = render(EngineSelectiveCard);
		expect((container.querySelector('input[type="checkbox"]') as HTMLInputElement).disabled).toBe(
			true,
		);
		await fireEvent.click(getByText('Установить ipset'));
		expect(installDeps).toHaveBeenCalledTimes(1);
	});

	// Пока статус не пришёл, «ipset нет» неотличимо от «ещё не знаем»: блокировать
	// тумблер на этом основании значит запрещать включение на холодной загрузке.
	it('пока статус не загружен, тумблер не блокируется отсутствием ipset', () => {
		selectiveBypass.status.set(null);
		const { container, queryByText } = render(EngineSelectiveCard);
		expect((container.querySelector('input[type="checkbox"]') as HTMLInputElement).disabled).toBe(
			false,
		);
		expect(queryByText('Установить ipset')).toBeNull();
	});

	// ── Включённое состояние ─────────────────────────────────────────────────

	it('включённый перехват показывает записи и время последней пересборки', () => {
		setSelectiveEnabled(true);
		selectiveBypass.applyStatus(sel({ entryCount: 512, lastRebuild: '2026-08-06T10:00:00Z' }));
		const { getByText, container } = render(EngineSelectiveCard);
		expect(getByText('Записей в ipset')).toBeTruthy();
		expect(getByText('512')).toBeTruthy();
		const values = [...container.querySelectorAll('.stat-value')].map((e) => e.textContent?.trim());
		expect(values).toHaveLength(2);
		expect(values[1]).not.toBe('—');
	});

	it('без времени пересборки показывается прочерк, а не «Invalid Date»', () => {
		setSelectiveEnabled(true);
		selectiveBypass.applyStatus(sel({ lastRebuild: undefined }));
		const { container } = render(EngineSelectiveCard);
		const values = [...container.querySelectorAll('.stat-value')].map((e) => e.textContent?.trim());
		expect(values[1]).toBe('—');
	});

	it('ошибка последней пересборки видна отдельной строкой', () => {
		setSelectiveEnabled(true);
		selectiveBypass.applyStatus(sel({ lastError: 'resolve timeout' }));
		const { getByText } = render(EngineSelectiveCard);
		expect(getByText(/Ошибка пересборки: resolve timeout/)).toBeTruthy();
	});

	it('выключенный перехват не показывает ни статистики, ни кнопки пересборки', () => {
		const { queryByText } = render(EngineSelectiveCard);
		expect(queryByText('Записей в ipset')).toBeNull();
		expect(queryByText('Пересобрать ipset')).toBeNull();
	});

	// Пересборку могли запустить НЕ отсюда: «Применить» в баннере, вторая
	// вкладка, авто-пересборка на старте. Если кнопка держится только на
	// локальном флаге, пользователь заходит на страницу, видит доступную кнопку
	// и жмёт её поверх идущей работы.
	it('пересборка, запущенная не отсюда, показывается на кнопке', () => {
		setSelectiveEnabled(true);
		selectiveBypass.applyStatus(sel({ rebuilding: true }));
		const { getByText, queryByText } = render(EngineSelectiveCard);
		expect(getByText('Пересборка…')).toBeTruthy();
		expect(queryByText('Пересобрать ipset')).toBeNull();
		expect(getByText('Пересборка…').closest('button')?.getAttribute('aria-busy')).toBe('true');
	});

	// Вторая половина того же: локальный флаг гаснет в finally сразу после 202,
	// и дальше «Пересборка…» держится ровно на status.rebuilding из тела ответа.
	it('после ответа 202 кнопка остаётся в пересборке по статусу, а не по флагу', async () => {
		setSelectiveEnabled(true);
		rebuild.mockResolvedValue(sel({ rebuilding: true }));
		const { getByText } = render(EngineSelectiveCard);
		await fireEvent.click(getByText('Пересобрать ipset'));
		await vi.waitFor(() => expect(get(selectiveBypass.status)?.rebuilding).toBe(true));
		expect(getByText('Пересборка…')).toBeTruthy();
	});

	it('завершение по SSE возвращает кнопку в исходное состояние', async () => {
		setSelectiveEnabled(true);
		selectiveBypass.applyStatus(sel({ rebuilding: true }));
		const { getByText, queryByText } = render(EngineSelectiveCard);
		expect(queryByText('Пересобрать ipset')).toBeNull();
		selectiveBypass.applyStatus(sel({ rebuilding: false }));
		await vi.waitFor(() => expect(getByText('Пересобрать ipset')).toBeTruthy());
	});

	it('кнопка «Пересобрать ipset» запускает пересборку и просит окно прогресса', async () => {
		setSelectiveEnabled(true);
		selectiveBypass.clearModalRequest();
		const { getByText } = render(EngineSelectiveCard);
		await fireEvent.click(getByText('Пересобрать ipset'));
		expect(rebuild).toHaveBeenCalledTimes(1);
		expect(get(selectiveBypass.modalRequested)).toBe(true);
	});

	// Кнопка обязана звать РУЧНОЙ триггер, а не молчаливый пост-Apply
	// (triggerSelectiveRebuildIfEnabled): тот глотает ошибки, и человек, нажавший
	// кнопку, не узнал бы ни про конфликт, ни про отказ.
	it('конфликт 409 во время пересборки объясняется тостом, а не молчанием', async () => {
		setSelectiveEnabled(true);
		rebuild.mockRejectedValue({ body: { code: 'OPERATION_IN_PROGRESS' } });
		const { getByText } = render(EngineSelectiveCard);
		await fireEvent.click(getByText('Пересобрать ipset'));
		await vi.waitFor(() =>
			expect(
				get(notifications).some(
					(n) => n.type === 'error' && /применяется конфигурация sing-box/.test(n.message),
				),
			).toBe(true),
		);
	});

	it('провалившаяся пересборка не остаётся незамеченной', async () => {
		setSelectiveEnabled(true);
		rebuild.mockRejectedValue(new Error('ipset: no such file'));
		const { getByText } = render(EngineSelectiveCard);
		await fireEvent.click(getByText('Пересобрать ipset'));
		await vi.waitFor(() =>
			expect(get(notifications).some((n) => /ipset: no such file/.test(n.message))).toBe(true),
		);
	});

	// Кнопка просмотра появляется только когда в снимке реально что-то есть:
	// пустая модалка ничего не объясняет.
	it('без снимка кнопки «Содержимое ipset» нет', () => {
		setSelectiveEnabled(true);
		selectiveBypass.applyStatus(sel({ snapshot: undefined }));
		expect(render(EngineSelectiveCard).queryByText(/Содержимое ipset/)).toBeNull();
	});

	it('пустой снимок кнопку не показывает — модалка была бы пустой', () => {
		setSelectiveEnabled(true);
		selectiveBypass.applyStatus(
			sel({
				snapshot: {
					rebuiltAt: '2026-08-06T10:00:00Z',
					staticCidrs: [],
					domainResults: [],
					entryCount: 0,
				},
			}),
		);
		expect(render(EngineSelectiveCard).queryByText(/Содержимое ipset/)).toBeNull();
	});

	it('непустой снимок открывается кнопкой', async () => {
		setSelectiveEnabled(true);
		selectiveBypass.applyStatus(
			sel({
				snapshot: {
					rebuiltAt: '2026-08-06T10:00:00Z',
					staticCidrs: [],
					domainResults: [],
					entryCount: 12,
				},
			}),
		);
		const { getByText } = render(EngineSelectiveCard);
		await fireEvent.click(getByText(/Содержимое ipset/));
		expect(document.querySelector('.modal-card')).not.toBeNull();
	});

	it('ссылка на журнал ведёт в приложенческий журнал, а не в журнал движка', () => {
		setSelectiveEnabled(true);
		const { getByText } = render(EngineSelectiveCard);
		expect(getByText('Журнал').getAttribute('href')).toBe('/logs');
	});

	// ── Тумблер ──────────────────────────────────────────────────────────────

	it('включение сохраняет настройку', async () => {
		const { container } = render(EngineSelectiveCard);
		await fireEvent.click(container.querySelector('input[type="checkbox"]') as HTMLInputElement);
		await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
		expect(putSettings.mock.calls[0][0]).toMatchObject({ selectiveBypass: true });
	});
});
