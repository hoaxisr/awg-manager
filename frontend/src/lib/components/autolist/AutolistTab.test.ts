import { cleanup, fireEvent, render, screen, within } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import AutolistTab from './AutolistTab.svelte';
import { m } from '$lib/i18n';
import { locale } from '$lib/i18n';
import { api } from '$lib/api/client';
import { autolistStatus } from '$lib/stores/autolist';
import { notifications } from '$lib/stores/notifications';
import type { AutolistCandidate, AutolistConfig, AutolistStatus } from '$lib/types';

function candidate(overrides: Partial<AutolistCandidate> = {}): AutolistCandidate {
	return {
		id: 'domain|example.com|tcp|443',
		target: { domain: 'example.com', host: 'www.example.com', ip: '93.184.216.34', network: 'tcp', port: 443 },
		reason: 'tcp-rst',
		state: 'waiting',
		firstAt: new Date().toISOString(),
		lastAt: new Date().toISOString(),
		coverage: { covered: false, durationMs: 12 },
		direct: { verdict: 'blocked', bytes: 0, durationMs: 5001, detail: 'curl: (28) Connection timed out' },
		wan: { verdict: 'reachable', bytes: 892, durationMs: 420 },
		tunnel: { verdict: 'reachable', bytes: 1204, durationMs: 610 },
		targetInterface: 'nwg0',
		targetOutbound: 'nwg',
		correlationID: 'c0007',
		...overrides,
	};
}

const runningStatus: AutolistStatus = {
	running: true,
	installed: true,
	enabled: true,
	captureActive: true,
	capturePaused: false,
	protocols: ['tcp/443', 'dns/53'],
	targetInterface: 'nwg0',
	targetOutbound: 'nwg',
	resolveVia: '',
	lastWAN: { verdict: 'reachable', durationMs: 410, checkedAt: new Date().toISOString() },
	activeChecks: 1,
	queuedChecks: 2,
	addsUsedHour: 3,
	addsLimit: 50,
	candidates: [candidate()],
	history: [],
};

const fullConfig: AutolistConfig = {
	autoAdd: false,
	targetInterface: 'nwg0',
	resolveVia: '',
	wanCheckUrl: 'https://ya.ru/robots.txt',
	wanMonitorEnabled: false,
	tunnelProbe: true,
	excludeDomains: [],
	excludeRuleSets: [],
	udpEnabled: false,
	verbose: false,
	addsPerHour: 50,
};

const emptyResources = () => {
	vi.spyOn(api, 'getAllInterfaces').mockResolvedValue([]);
	vi.spyOn(api, 'singboxRouterListDNSServers').mockResolvedValue([]);
	vi.spyOn(api, 'singboxRouterGetDNSGlobals').mockResolvedValue({ final: 'dns-final', strategy: 'prefer_ipv4' });
	vi.spyOn(api, 'singboxRouterListRuleSets').mockResolvedValue([]);
};

/** Только DNS-ресурсы пусты: список интерфейсов задаёт сам тест. */
const emptyDnsResources = () => {
	vi.spyOn(api, 'singboxRouterListDNSServers').mockResolvedValue([]);
	vi.spyOn(api, 'singboxRouterGetDNSGlobals').mockResolvedValue({ final: 'dns-final', strategy: 'prefer_ipv4' });
	vi.spyOn(api, 'singboxRouterListRuleSets').mockResolvedValue([]);
};

/** Только каталог интерфейсов пуст: DNS-серверы задаёт сам тест. */
const emptyInterfaceResources = () => {
	vi.spyOn(api, 'getAllInterfaces').mockResolvedValue([]);
	vi.spyOn(api, 'singboxRouterListRuleSets').mockResolvedValue([]);
};

/** Tab buttons carry a badge count in their accessible name — match by prefix. */
function regexEscape(s: string): string {
	return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

beforeEach(() => {
	vi.stubGlobal('ResizeObserver', class {
		observe() {}
		disconnect() {}
	});
	vi.spyOn(notifications, 'success').mockImplementation(() => '');
	vi.spyOn(notifications, 'error').mockImplementation(() => '');
	vi.spyOn(notifications, 'warning').mockImplementation(() => '');
	vi.spyOn(notifications, 'info').mockImplementation(() => '');
});

afterEach(() => {
	cleanup();
	autolistStatus.invalidate();
	vi.restoreAllMocks();
	vi.unstubAllGlobals();
});

describe('AutolistTab — lifecycle', () => {
	it('offers install when the package is absent and calls the install API', async () => {
		const notInstalled: AutolistStatus = { ...runningStatus, running: false, installed: false, enabled: false };
		const statusSpy = vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(notInstalled);
		const install = vi.spyOn(api, 'installAutolist').mockImplementation(async () => {
			statusSpy.mockResolvedValue(runningStatus);
			return runningStatus;
		});

		render(AutolistTab);
		await screen.findByText(m.autolist_not_installed());
		await fireEvent.click(screen.getByRole('button', { name: m.autolist_install() }));

		expect(install).toHaveBeenCalledOnce();
		await vi.waitFor(() => {
			expect(screen.queryByText(m.autolist_not_installed())).toBeNull();
			expect(screen.getAllByText(m.autolist_capture_active()).length).toBeGreaterThan(0);
		});
	});

	it('offers enable for an installed-but-disabled satellite', async () => {
		vi.spyOn(api, 'getAutolistStatus')
			.mockResolvedValueOnce({ ...runningStatus, running: false, enabled: false })
			.mockResolvedValue(runningStatus);
		const control = vi.spyOn(api, 'controlAutolist').mockResolvedValue(runningStatus);

		render(AutolistTab);
		await fireEvent.click(await screen.findByRole('button', { name: m.autolist_enable() }));

		expect(control).toHaveBeenCalledWith('enable');
		await vi.waitFor(() => expect(screen.getByText(m.autolist_capture_active())).toBeTruthy());
	});

	it('renders the header disable button with the primary accent variant and no confirmation', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);
		const control = vi.spyOn(api, 'controlAutolist').mockResolvedValue(runningStatus);

		render(AutolistTab);
		const disable = await screen.findByRole('button', { name: m.autolist_disable() });

		// Акцентная кнопка — variant-primary, никакого подтверждающего модала.
		expect(disable.className).toContain('variant-primary');
		await fireEvent.click(disable);

		expect(control).toHaveBeenCalledWith('disable');
	});
});

describe('AutolistTab — main screen', () => {
	it('shows capture state, target, DNS, WAN verdict and the hourly budget', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);

		render(AutolistTab);
		await screen.findByText('example.com');

		expect(screen.getByText(m.autolist_capture_active())).toBeTruthy();
		expect(screen.getByText(m.autolist_dns_final())).toBeTruthy();
		expect(screen.getByText(`3 ${m.autolist_adds_limit({ limit: 50 })}`)).toBeTruthy();
	});

	it('shows the tunnel stat as human-readable label plus kernel name from the interface catalog', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);
		vi.spyOn(api, 'getAllInterfaces').mockResolvedValue([
			{ name: 'nwg0', label: 'keenetic-home-ocserv', up: true, securityLevel: 'public' },
		]);
		emptyDnsResources();

		render(AutolistTab);
		// value = человекочитаемое имя · kernel-имя; outbound остаётся в sub.
		await screen.findByText('keenetic-home-ocserv · nwg0');
		expect(screen.getByText('→ nwg')).toBeTruthy();
	});

	it('keeps the bare kernel name when the catalog has no label for the interface', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue({ ...runningStatus, targetInterface: 'ppp0' });
		vi.spyOn(api, 'getAllInterfaces').mockResolvedValue([
			{ name: 'ppp0', label: '', up: true, securityLevel: 'public' },
		]);
		emptyDnsResources();

		render(AutolistTab);
		// value = ppp0 (без «·»), sub не показывается.
		await screen.findByText('ppp0');
	});

	it('marks the capture as paused when the WAN monitor holds it', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue({ ...runningStatus, captureActive: false, capturePaused: true });

		render(AutolistTab);
		await screen.findByText('example.com');

		expect(screen.getByText(m.autolist_capture_paused())).toBeTruthy();
	});
});

describe('AutolistTab — DNS stat description', () => {
	it('describes a named sb: server as transport · address with lowercase transport', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue({ ...runningStatus, resolveVia: 'sb:quad9' });
		emptyInterfaceResources();
		vi.spyOn(api, 'singboxRouterListDNSServers').mockResolvedValue([
			{ tag: 'quad9', type: 'https', server: '9.9.9.9', server_port: 443 },
		]);
		vi.spyOn(api, 'singboxRouterGetDNSGlobals').mockResolvedValue({ final: 'dns-final', strategy: 'prefer_ipv4' });
		vi.spyOn(api, 'singboxRouterListRuleSets').mockResolvedValue([]);

		render(AutolistTab);
		await screen.findByText('quad9');
		expect(screen.getByText('https · 9.9.9.9')).toBeTruthy();
	});

	it('resolves the effective final server for the empty resolveVia', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);
		emptyInterfaceResources();
		vi.spyOn(api, 'singboxRouterListDNSServers').mockResolvedValue([
			{ tag: 'dns-final', type: 'tls', server: '9.9.9.9', server_port: 853 },
		]);
		vi.spyOn(api, 'singboxRouterGetDNSGlobals').mockResolvedValue({ final: 'dns-final', strategy: 'prefer_ipv4' });
		vi.spyOn(api, 'singboxRouterListRuleSets').mockResolvedValue([]);

		render(AutolistTab);
		// value — «sing-box dns.final», sub — effective final сервер.
		await screen.findByText(m.autolist_dns_final());
		expect(screen.getByText('tls · 9.9.9.9')).toBeTruthy();
	});

	it('omits the description when the final tag has no known server and keeps the tag for an unknown sb: server', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue({ ...runningStatus, resolveVia: 'sb:ghost' });
		emptyInterfaceResources();
		vi.spyOn(api, 'singboxRouterListDNSServers').mockResolvedValue([]);
		vi.spyOn(api, 'singboxRouterGetDNSGlobals').mockResolvedValue({ final: '', strategy: 'prefer_ipv4' });
		vi.spyOn(api, 'singboxRouterListRuleSets').mockResolvedValue([]);

		render(AutolistTab);
		await screen.findByText('ghost');
		// Никаких «undefined» или пустых строк-описаний.
		expect(screen.queryByText(/· 9\.9\.9\.9/)).toBeNull();
	});
});

describe('AutolistTab — candidate actions', () => {
	it('adds a waiting candidate and refreshes the status', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);
		const add = vi.spyOn(api, 'addAutolistCandidate').mockResolvedValue();

		render(AutolistTab);
		await screen.findByText('example.com');
		await fireEvent.click(screen.getByRole('button', { name: m.autolist_action_add() }));

		expect(add).toHaveBeenCalledWith('domain|example.com|tcp|443');
	});

	it('shows the forget action with the CircleX icon and the dismiss action with EyeOff', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);

		render(AutolistTab);
		await screen.findByText('example.com');

		const forget = screen.getByRole('button', { name: m.autolist_action_forget() });
		expect(forget.querySelector('svg.lucide-circle-x')).toBeTruthy();
		expect(forget.querySelector('svg.lucide-circle-alert')).toBeNull();

		const dismiss = screen.getByRole('button', { name: m.autolist_action_dismiss() });
		expect(dismiss.querySelector('svg.lucide-eye-off')).toBeTruthy();
	});

	it('orders the evidence into three columns: Reason/WAN, Direct/Tunnel, First seen/Last seen', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);

		render(AutolistTab);
		await screen.findByText('example.com');

		const evidence = document.querySelector('.autolist-evidence');
		expect(evidence).toBeTruthy();
		const columns = evidence!.querySelectorAll(':scope > div');
		expect(columns).toHaveLength(3);
		const text = (el: Element) => el.textContent ?? '';
		// column 1: Reason then WAN
		expect(text(columns[0])).toContain(m.autolist_col_reason());
		expect(text(columns[0])).toContain(m.autolist_col_wan());
		expect(text(columns[0])).not.toContain(m.autolist_col_direct());
		// column 2: Direct then Tunnel
		expect(text(columns[1])).toContain(m.autolist_col_direct());
		expect(text(columns[1])).toContain(m.autolist_col_tunnel());
		expect(text(columns[1])).not.toContain(m.autolist_col_reason());
		// column 3: First seen then Last seen
		expect(text(columns[2])).toContain(m.autolist_col_first_seen());
		expect(text(columns[2])).toContain(m.autolist_col_last_seen());
	});

	it('keeps the satellite version in the header instead of the meta line', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue({ ...runningStatus, version: '0.9.0' });

		render(AutolistTab);
		await screen.findByText('example.com');

		// Версия живёт в шапке рядом с «Сателлит работает».
		const runningBadge = screen.getByText(m.autolist_running()).closest('span');
		expect(runningBadge?.textContent).toContain(m.autolist_version());
		expect(runningBadge?.textContent).toContain('0.9.0');
		// В мета-строке под статами версии нет: там только бейдж исчерпания лимита.
		const metaLine = Array.from(document.querySelectorAll('.flex.flex-wrap.items-center.gap-x-6'))
			.filter((el) => !el.closest('.page-header') && !el.matches('.autolist-controls, .autolist-controls *'))
			.map((el) => el.textContent ?? '')
			.join('\n');
		expect(metaLine).not.toContain(m.autolist_version());
		expect(metaLine).not.toContain('0.9.0');
	});

	it('offers explicit remove for an added candidate instead of add', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue({
			...runningStatus,
			candidates: [candidate({ id: 'domain|added.example|tcp|443', target: { domain: 'added.example', network: 'tcp', port: 443 }, state: 'added' })],
		});
		const remove = vi.spyOn(api, 'removeAutolistCandidate').mockResolvedValue();

		render(AutolistTab);
		// Добавленные цели живут на отдельной вкладке Added.
		await fireEvent.click(await screen.findByRole('button', { name: new RegExp(regexEscape(m.autolist_view_added())) }));
		await screen.findByText('added.example');

		expect(screen.queryByRole('button', { name: m.autolist_action_add() })).toBeNull();
		expect(screen.queryByRole('button', { name: m.autolist_action_forget() })).toBeNull();
		await fireEvent.click(screen.getByRole('button', { name: m.autolist_action_remove() }));
		expect(remove).toHaveBeenCalledWith('domain|added.example|tcp|443');
	});

	it('splits candidates into Candidates, Added and History tabs', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue({
			...runningStatus,
			candidates: [
				candidate(),
				candidate({ id: 'domain|added.example|tcp|443', target: { domain: 'added.example', network: 'tcp', port: 443 }, state: 'added' }),
			],
		});

		render(AutolistTab);
		await screen.findByText('example.com');
		// added-карточка не мешает вкладке кандидатов
		expect(screen.queryByText('added.example')).toBeNull();

		await fireEvent.click(screen.getByRole('button', { name: new RegExp(regexEscape(m.autolist_view_added())) }));
		expect(screen.getByText('added.example')).toBeTruthy();
		// на Added нет выбора и массовых кнопок
		expect(screen.queryByRole('button', { name: m.autolist_add_selected() })).toBeNull();
		// история — отдельная вкладка-журнал
		await fireEvent.click(screen.getByRole('button', { name: new RegExp(regexEscape(m.autolist_view_history())) }));
		expect(screen.getByText(m.autolist_history_empty())).toBeTruthy();
	});

	it('keeps technical probe details collapsed until expanded', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);

		render(AutolistTab);
		await screen.findByText('example.com');

		const details = screen.getByText(m.autolist_details()).closest('details');
		expect(details?.open).toBe(false);

		await fireEvent.click(screen.getByText(m.autolist_details()));
		expect(details?.open).toBe(true);
		await screen.findByText('curl: (28) Connection timed out');
		expect(details!.textContent).toContain(m.autolist_details_correlation());
		expect(details!.textContent).toContain('c0007');
	});
});

describe('AutolistTab — bulk actions', () => {
	function bulkStatus(cands: AutolistCandidate[]): AutolistStatus {
		return { ...runningStatus, candidates: cands };
	}

	const ipCandidate = (): AutolistCandidate =>
		candidate({
			id: 'ip|198.51.100.7',
			target: { ip: '198.51.100.7', network: 'udp', port: 443 },
		});

	async function selectTwo(rendered: Awaited<ReturnType<typeof render<typeof AutolistTab>>>) {
		const boxes = rendered.container.querySelectorAll<HTMLInputElement>('.card input[type="checkbox"]');
		await fireEvent.click(boxes[0]);
		await fireEvent.click(boxes[1]);
	}

	it('selects all candidates, shows the selected count and batch-adds by candidate ID', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(
			bulkStatus([candidate(), candidate({ id: 'domain|second.example|tcp|443', target: { domain: 'second.example', network: 'tcp', port: 443 } })]),
		);
		const batchAdd = vi.spyOn(api, 'addAutolistCandidates').mockResolvedValue({ ok: [], failed: [] });

		const rendered = render(AutolistTab);
		await screen.findByText('example.com');

		expect(screen.queryByText(m.autolist_selected({ count: 0 }))).toBeTruthy();
		await fireEvent.click(screen.getByRole('checkbox', { name: m.autolist_select_all() }));

		expect(screen.getByText(m.autolist_selected({ count: 2 }))).toBeTruthy();
		await fireEvent.click(screen.getByRole('button', { name: m.autolist_add_selected() }));

		await vi.waitFor(() =>
			expect(batchAdd).toHaveBeenCalledWith(['domain|example.com|tcp|443', 'domain|second.example|tcp|443']),
		);
		void selectTwo; // helper used by other tests
	});

	it('hides selected candidates in one batch call with per-candidate IDs', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(bulkStatus([candidate(), ipCandidate()]));
		const batchHide = vi.spyOn(api, 'dismissAutolistCandidates').mockResolvedValue({ ok: [], failed: [] });

		const rendered = render(AutolistTab);
		await screen.findByText('example.com');
		await selectTwo(rendered);
		await fireEvent.click(screen.getByRole('button', { name: m.autolist_hide_selected() }));

		await vi.waitFor(() =>
			expect(batchHide).toHaveBeenCalledWith(['domain|example.com|tcp|443', 'ip|198.51.100.7']),
		);
	});

	it('surfaces per-ID batch failures as an error list, not a success toast', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(bulkStatus([candidate(), ipCandidate()]));
		vi.spyOn(api, 'addAutolistCandidates').mockResolvedValue({
			ok: ['domain|example.com|tcp|443'],
			failed: [{ id: 'ip|198.51.100.7', error: 'apply failed after content update' }],
		});

		const rendered = render(AutolistTab);
		await screen.findByText('example.com');
		await selectTwo(rendered);
		await fireEvent.click(screen.getByRole('button', { name: m.autolist_add_selected() }));

		await vi.waitFor(() => expect(screen.getByText('ip|198.51.100.7')).toBeTruthy());
		expect(screen.getByText(/apply failed after content update/)).toBeTruthy();
	});

	it('blocks Disable forever with a clear error when an IP-only candidate is selected', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(bulkStatus([candidate(), ipCandidate()]));
		const getConfig = vi.spyOn(api, 'getAutolistConfig');
		const setConfig = vi.spyOn(api, 'setAutolistConfig').mockResolvedValue();

		const rendered = render(AutolistTab);
		await screen.findByText('example.com');
		await selectTwo(rendered);
		await fireEvent.click(screen.getByRole('button', { name: m.autolist_disable_forever() }));

		// Конфиг не читаем и не пишем: ошибка валидации до любого запроса.
		expect(getConfig).not.toHaveBeenCalled();
		expect(setConfig).not.toHaveBeenCalled();
		await vi.waitFor(() =>
			expect(notifications.error).toHaveBeenCalledWith(m.autolist_bulk_exclude_ip()),
		);
	});

	it('Disable forever merges domains into the live config, then batch-forgets the cards', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(
			bulkStatus([candidate(), candidate({ id: 'domain|second.example|tcp|443', target: { domain: 'second.example', network: 'tcp', port: 443 } })]),
		);
		vi.spyOn(api, 'getAutolistConfig').mockResolvedValue({ ...fullConfig, excludeDomains: ['keep.example'] });
		const setConfig = vi.spyOn(api, 'setAutolistConfig').mockResolvedValue();
		vi.spyOn(api, 'forgetAutolistCandidates')
			.mockResolvedValue({ ok: ['domain|example.com|tcp|443'], failed: [{ id: 'domain|second.example|tcp|443', error: 'not found' }] });

		const rendered = render(AutolistTab);
		await screen.findByText('example.com');
		await selectTwo(rendered);
		await fireEvent.click(screen.getByRole('button', { name: m.autolist_disable_forever() }));

		await vi.waitFor(() => expect(setConfig).toHaveBeenCalledTimes(1));
		const sent = setConfig.mock.calls[0][0] as unknown as { excludeDomains: string[] };
		expect([...sent.excludeDomains].sort()).toEqual(['example.com', 'keep.example', 'second.example']);

		// Конфиг сохранён, forget частично упал — сообщаем об этом, не молчим.
		await vi.waitFor(() =>
			expect(notifications.warning).toHaveBeenCalledWith(m.autolist_bulk_exclude_forget_err()),
		);
	});
});

describe('AutolistTab — history', () => {
	it('renders add and remove records with source and result', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue({
			...runningStatus,
			history: [
				{
					at: new Date().toISOString(),
					domain: 'example.com',
					network: 'tcp',
					port: 443,
					action: 'added',
					source: 'auto',
					interface: 'nwg0',
					outbound: 'nwg',
					reason: 'tcp-rst',
					result: 'ok',
				},
				{
					at: new Date().toISOString(),
					domain: 'old.example.org',
					action: 'removed',
					source: 'user',
					result: 'error: readback failed',
				},
			],
		});

		render(AutolistTab);
		await screen.findByText('example.com');
		await fireEvent.click(screen.getByRole('button', { name: new RegExp(regexEscape(m.autolist_view_history())) }));

		expect(screen.getByText(m.autolist_history_added())).toBeTruthy();
		expect(screen.getByText(m.autolist_history_removed())).toBeTruthy();
		expect(screen.getByText(m.autolist_source_auto())).toBeTruthy();
		expect(screen.getAllByText(/readback failed/).length).toBeGreaterThan(0);
	});

	it('restores a dismissed entry by its exact candidate ID, not by removed history', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue({
			...runningStatus,
			history: [
				{
					at: new Date().toISOString(), domain: 'example.com', network: 'tcp', port: 443,
					action: 'dismissed', source: 'user', result: 'ok',
				},
				{
					at: new Date().toISOString(), domain: 'removed.example', action: 'removed', source: 'user', result: 'ok',
				},
			],
		});
		const restore = vi.spyOn(api, 'restoreAutolistCandidate').mockResolvedValue();
		render(AutolistTab);
		await screen.findByText('example.com');
		await fireEvent.click(screen.getByRole('button', { name: new RegExp(regexEscape(m.autolist_view_history())) }));
		const buttons = screen.getAllByRole('button', { name: m.autolist_action_restore() });
		expect(buttons).toHaveLength(1);
		await fireEvent.click(buttons[0]);
		await vi.waitFor(() => expect(restore).toHaveBeenCalledWith('domain|example.com|tcp|443'));
	});
});

describe('AutolistTab — settings', () => {
	it.each([
		{ name: 'omits config data', body: { success: true } },
		{ name: 'returns null config data', body: { success: true, data: null } },
	])('blocks saving and automatic-add confirmation when the config response $name', async ({ body }) => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);
		const getConfig = vi.spyOn(api, 'getAutolistConfig');
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(body), {
			status: 200,
			headers: { 'Content-Type': 'application/json' },
		})));
		emptyResources();
		const save = vi.spyOn(api, 'setAutolistConfig').mockResolvedValue();

		render(AutolistTab);
		await fireEvent.click(await screen.findByRole('button', { name: m.autolist_settings() }));
		await vi.waitFor(() => expect(getConfig).toHaveBeenCalledTimes(1));
		await expect(getConfig.mock.results[0].value).rejects.toThrow();

		const autoToggle = await screen.findByRole('checkbox', { name: m.autolist_cfg_auto() });
		const saveButton = screen.getByRole('button', { name: m.common_save() });
		expect(autoToggle.hasAttribute('disabled')).toBe(true);
		expect(saveButton.hasAttribute('disabled')).toBe(true);
		await fireEvent.click(autoToggle);
		await fireEvent.click(saveButton);
		expect(screen.queryByRole('button', { name: m.autolist_auto_confirm_yes() })).toBeNull();
		expect(save).not.toHaveBeenCalled();
	});

	it('disables the automatic-add toggle entirely while the tunnel probe is off', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);
		vi.spyOn(api, 'getAutolistConfig').mockResolvedValue({ ...fullConfig, tunnelProbe: false });
		emptyResources();
		const save = vi.spyOn(api, 'setAutolistConfig').mockResolvedValue();

		render(AutolistTab);
		await fireEvent.click(await screen.findByRole('button', { name: m.autolist_settings() }));
		const autoToggle = await screen.findByRole('checkbox', { name: m.autolist_cfg_auto() });

		expect(autoToggle.hasAttribute('disabled')).toBe(true);
		await fireEvent.click(autoToggle);
		expect(screen.queryByRole('button', { name: m.autolist_auto_confirm_yes() })).toBeNull();

		await fireEvent.click(screen.getByRole('button', { name: m.common_save() }));
		await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
		expect((save.mock.calls[0][0]).autoAdd).toBe(false);
		expect((save.mock.calls[0][0]).tunnelProbe).toBe(false);
	});

	it('turning the tunnel probe off clears autoAdd in the draft; re-enabling does not restore it', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);
		vi.spyOn(api, 'getAutolistConfig').mockResolvedValue({ ...fullConfig, autoAdd: true });
		emptyResources();
		const save = vi.spyOn(api, 'setAutolistConfig').mockResolvedValue();

		render(AutolistTab);
		await fireEvent.click(await screen.findByRole('button', { name: m.autolist_settings() }));
		const tunnelToggle = await screen.findByRole('checkbox', { name: m.autolist_cfg_tunnel_probe() });
		const autoToggle = await screen.findByRole('checkbox', { name: m.autolist_cfg_auto() });

		// autoAdd=true прочитан, tunnel выключаем — autoAdd гаснет.
		await fireEvent.click(tunnelToggle);
		expect(autoToggle.hasAttribute('disabled')).toBe(true);

		// Включаем tunnel обратно — autoAdd сам не возвращается.
		await fireEvent.click(tunnelToggle);
		expect(autoToggle.hasAttribute('disabled')).toBe(false);
		expect((autoToggle as HTMLInputElement).checked).toBe(false);

		await fireEvent.click(screen.getByRole('button', { name: m.common_save() }));
		await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
		expect((save.mock.calls[0][0]).autoAdd).toBe(false);
		expect((save.mock.calls[0][0]).tunnelProbe).toBe(true);
	});

	it('saves exactly the eleven 0.9.0 settings and closes the modal on success', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);
		vi.spyOn(api, 'getAutolistConfig').mockResolvedValue(fullConfig);
		emptyResources();
		const save = vi.spyOn(api, 'setAutolistConfig').mockResolvedValue();

		render(AutolistTab);
		await fireEvent.click(await screen.findByRole('button', { name: m.autolist_settings() }));
		const wanInput = await screen.findByLabelText(m.autolist_cfg_wan_url());
		await fireEvent.input(wanInput, { target: { value: 'https://example.com/health' } });
		await fireEvent.click(screen.getByRole('button', { name: m.common_save() }));

		await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
		const sent = save.mock.calls[0][0] as unknown as Record<string, unknown>;
		expect(Object.keys(sent).sort()).toEqual([
			'addsPerHour',
			'autoAdd',
			'excludeDomains',
			'excludeRuleSets',
			'resolveVia',
			'targetInterface',
			'tunnelProbe',
			'udpEnabled',
			'verbose',
			'wanCheckUrl',
			'wanMonitorEnabled',
		]);
		expect(sent.wanCheckUrl).toBe('https://example.com/health');
		expect(sent.addsPerHour).toBe(50);
		await vi.waitFor(() => expect(screen.queryByRole('button', { name: m.common_save() })).toBeNull());
	});

	it('sends the edited additions-per-hour value as a number in the config payload', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);
		vi.spyOn(api, 'getAutolistConfig').mockResolvedValue(fullConfig);
		emptyResources();
		const save = vi.spyOn(api, 'setAutolistConfig').mockResolvedValue();

		render(AutolistTab);
		await fireEvent.click(await screen.findByRole('button', { name: m.autolist_settings() }));
		const addsInput = await screen.findByLabelText(m.autolist_cfg_adds_per_hour());
		// Input отдаёт строку даже с type="number".
		await fireEvent.input(addsInput, { target: { value: '120' } });
		await fireEvent.click(screen.getByRole('button', { name: m.common_save() }));

		await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
		expect((save.mock.calls[0][0]).addsPerHour).toBe(120);
	});

	it('falls back to the previous draft when the additions-per-hour input is cleared or non-numeric', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);
		vi.spyOn(api, 'getAutolistConfig').mockResolvedValue(fullConfig);
		emptyResources();
		const save = vi.spyOn(api, 'setAutolistConfig').mockResolvedValue();

		render(AutolistTab);
		await fireEvent.click(await screen.findByRole('button', { name: m.autolist_settings() }));
		const addsInput = await screen.findByLabelText(m.autolist_cfg_adds_per_hour());
		await fireEvent.input(addsInput, { target: { value: '' } });
		await fireEvent.click(screen.getByRole('button', { name: m.common_save() }));

		await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
		// Пустой ввод не переписывает черновик: уходит прочитанное 50.
		expect((save.mock.calls[0][0]).addsPerHour).toBe(50);
	});

	it('applies the default 50 additions/hour when the saved config omits the field', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);
		vi.spyOn(api, 'getAutolistConfig').mockResolvedValue({ ...fullConfig, addsPerHour: undefined });
		emptyResources();
		const save = vi.spyOn(api, 'setAutolistConfig').mockResolvedValue();

		render(AutolistTab);
		await fireEvent.click(await screen.findByRole('button', { name: m.autolist_settings() }));
		const addsInput = await screen.findByLabelText(m.autolist_cfg_adds_per_hour());
		expect((addsInput as HTMLInputElement).value).toBe('50');

		await fireEvent.click(screen.getByRole('button', { name: m.common_save() }));
		await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
		expect((save.mock.calls[0][0]).addsPerHour).toBe(50);
	});
});

describe('AutolistTab — clear history', () => {
	it('shows a danger block in settings and clears history after a red confirmation', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);
		vi.spyOn(api, 'getAutolistConfig').mockResolvedValue(fullConfig);
		emptyResources();
		const clear = vi.spyOn(api, 'clearAutolistHistory').mockResolvedValue();

		render(AutolistTab);
		await fireEvent.click(await screen.findByRole('button', { name: m.autolist_settings() }));

		// Опасный блок с иконкой корзины и кнопкой очистки.
		const block = await screen.findByText(m.autolist_history_clear_hint());
		expect(block.closest('.autolist-warning')?.className).toContain('bg-error');
		expect(block.closest('.autolist-warning')!.querySelector('svg.lucide-trash-2')).toBeTruthy();

		await fireEvent.click(screen.getByRole('button', { name: m.autolist_history_clear() }));
		expect(clear).not.toHaveBeenCalled();

		// Красный подтверждающий модал: danger-кнопка подтверждения.
		const confirmTitle = await screen.findByText(m.autolist_history_clear_confirm_title());
		const confirmModal = confirmTitle.closest('.modal-backdrop') as HTMLElement;
		expect(confirmModal).toBeTruthy();
		const confirmButton = within(confirmModal).getByRole('button', { name: m.autolist_history_clear_confirm_yes() });
		expect(confirmButton.className).toContain('variant-danger');

		await fireEvent.click(confirmButton);
		await vi.waitFor(() => expect(clear).toHaveBeenCalledTimes(1));
		expect(notifications.success).toHaveBeenCalledWith(m.autolist_history_clear_ok());
		// После очистки подтягиваем статус: журнал в StatStrip/вкладке пустеет.
		await vi.waitFor(() => expect(screen.queryByText(m.autolist_history_clear_confirm_title())).toBeNull());
	});

	it('cancel does nothing: no API call, history stays, confirmation just closes', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);
		vi.spyOn(api, 'getAutolistConfig').mockResolvedValue(fullConfig);
		emptyResources();
		const clear = vi.spyOn(api, 'clearAutolistHistory').mockResolvedValue();

		render(AutolistTab);
		await fireEvent.click(await screen.findByRole('button', { name: m.autolist_settings() }));
		await fireEvent.click(await screen.findByRole('button', { name: m.autolist_history_clear() }));
		const confirmTitle = await screen.findByText(m.autolist_history_clear_confirm_title());
		const confirmModal = confirmTitle.closest('.modal-backdrop') as HTMLElement;

		await fireEvent.click(within(confirmModal).getByRole('button', { name: m.common_cancel() }));

		expect(screen.queryByText(m.autolist_history_clear_confirm_title())).toBeNull();
		expect(clear).not.toHaveBeenCalled();
		expect(notifications.error).not.toHaveBeenCalled();
	});

	it('reports a failed clear through the error notification and keeps the modal flow usable', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);
		vi.spyOn(api, 'getAutolistConfig').mockResolvedValue(fullConfig);
		emptyResources();
		vi.spyOn(api, 'clearAutolistHistory').mockRejectedValue(new Error('satellite is not answering'));

		render(AutolistTab);
		await fireEvent.click(await screen.findByRole('button', { name: m.autolist_settings() }));
		await fireEvent.click(await screen.findByRole('button', { name: m.autolist_history_clear() }));
		const confirmTitle = await screen.findByText(m.autolist_history_clear_confirm_title());
		const confirmModal = confirmTitle.closest('.modal-backdrop') as HTMLElement;

		await fireEvent.click(within(confirmModal).getByRole('button', { name: m.autolist_history_clear_confirm_yes() }));

		await vi.waitFor(() => expect(notifications.error).toHaveBeenCalledWith('satellite is not answering'));
	});
});

describe('AutolistTab — new settings strings follow the locale (ru/en)', () => {
	afterEach(() => {
		locale.set('ru');
	});

	it('history-clear block, confirmation and additions-per-hour render in ru and switch to en', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(runningStatus);
		vi.spyOn(api, 'getAutolistConfig').mockResolvedValue(fullConfig);
		emptyResources();

		render(AutolistTab);
		await fireEvent.click(await screen.findByRole('button', { name: 'Настройки' }));

		// Русские строки опасного блока и поля бюджета. Заголовок совпадает
		// с подписью кнопки — проверяем по вхождениям.
		expect(screen.getAllByText('Очистить историю').length).toBe(2);
		expect(screen.getByText('Удаляет только записи журнала. Кандидаты, правила и бюджет добавлений не затрагиваются.')).toBeTruthy();
		const addsInput = screen.getByLabelText('Добавлений в час');
		expect((addsInput as HTMLInputElement).value).toBe('50');

		// Подтверждение: русские заголовок и текст. После открытия модал
		// добавляет третью одноимённую кнопку-подтверждение.
		await fireEvent.click(screen.getByRole('button', { name: 'Очистить историю' }));
		expect(screen.getAllByText('Очистить историю').length).toBe(3);
		expect(screen.getByText('Очистить историю?')).toBeTruthy();
		expect(screen.getByText('Все записи журнала будут удалены безвозвратно. Кандидаты, правила и бюджет добавлений не затрагиваются.')).toBeTruthy();

		locale.set('en');
		flushSync();

		// Английские строки той же модалки настроек.
		expect(screen.getAllByText('Clear history').length).toBe(3);
		expect(screen.getByText('Removes journal records only. Candidates, rules and the additions budget are not touched.')).toBeTruthy();
		expect(screen.getByLabelText('Additions per hour')).toBeTruthy();
		// Подтверждение перерисовалось на английский без перемонтирования.
		expect(screen.getByText('Clear history?')).toBeTruthy();
		expect(screen.getByText('All journal records will be deleted permanently. Candidates, rules and the additions budget are not touched.')).toBeTruthy();
		// Обе кнопки (блок + подтверждение) перевелись.
		expect(screen.getAllByRole('button', { name: 'Clear history' }).length).toBe(2);
	});
});
