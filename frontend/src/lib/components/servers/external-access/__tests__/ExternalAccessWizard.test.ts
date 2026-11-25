import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import { api } from '$lib/api/client';
import type { PreflightResponse, WizardPlanRequest } from '$lib/types/serverWizard';
import type { TgWebProxyConfig, XrayConfig } from '$lib/types';
import type { XrayRealityState } from '$lib/types/xray';
import ExternalAccessWizard from '../ExternalAccessWizard.svelte';

vi.mock('$lib/api/client', () => ({
	api: {
		getXrayReality: vi.fn(),
		getWizardCapabilities: vi.fn(),
		runWizardPreflight: vi.fn(),
		createWizardPlan: vi.fn(),
		applyWizardPlan: vi.fn(),
		getWizardJob: vi.fn(),
		cancelWizardJob: vi.fn(),
	},
}));
vi.mock('$lib/stores/notifications', () => ({
	notifications: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

type Job = Awaited<ReturnType<typeof api.getWizardJob>>;

const PREFLIGHT_OK: PreflightResponse = { can_proceed: true, has_warnings: false, checks: [], fingerprint: '' };

let plans = 0;

function tgConfig(over: Partial<TgWebProxyConfig> = {}): TgWebProxyConfig {
	return {
		enabled: true, scenario: 'direct_fake_tls', listen_port: 8085, admin_port: 8086, public_hostname: '',
		direct_host: '203.0.113.10', direct_port: 8443, tls_domain: 'www.google.com', carrier_mode: 'websocket',
		upstream_device: 'direct', secret: '00112233445566778899aabbccddeeff', backend: '127.0.0.1:2398',
		...over,
	};
}

function mount(cfg = tgConfig(), xrayConfig: XrayConfig | null = null) {
	const onclose = vi.fn();
	render(ExternalAccessWizard, {
		props: {
			open: true, initialKind: 'tgwebproxy', onclose,
			initialInput: { xrayConfig, xrayStatus: null, xrayTuning: null, tgConfig: cfg, tgStatus: null, dispatcherConfig: null },
		},
	});
	return { onclose };
}

const button = (name: string) => screen.queryByRole<HTMLButtonElement>('button', { name });

async function next() {
	await waitFor(() => expect(button(m.proxy_wizard_next())?.disabled).toBe(false));
	await fireEvent.click(button(m.proxy_wizard_next())!);
}

async function toReview() {
	for (let i = 0; i < 4; i++) await next();
	await waitFor(() => expect(button(m.common_apply())?.disabled).toBe(false));
}

describe('ExternalAccessWizard', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		plans = 0;
		vi.mocked(api.getXrayReality).mockResolvedValue(null as unknown as XrayRealityState);
		vi.mocked(api.getWizardCapabilities).mockResolvedValue({
			egress_options: [{ id: 'direct', kind: 'direct', name: 'WAN', iface: 'direct', available: true }],
		} as unknown as Awaited<ReturnType<typeof api.getWizardCapabilities>>);
		vi.mocked(api.runWizardPreflight).mockResolvedValue(PREFLIGHT_OK);
		vi.mocked(api.createWizardPlan).mockImplementation(
			async (_kind, request: WizardPlanRequest) =>
				({ plan_id: `plan-${++plans}`, request, plan: { summary: '', items: [], restart_required: true, state_fingerprint: '' } }) as unknown as Awaited<ReturnType<typeof api.createWizardPlan>>
		);
		vi.mocked(api.applyWizardPlan).mockResolvedValue({ job_id: 'job-1' });
	});
	afterEach(cleanup);

	// Мутации: убрать jobStopped из finish; не сбрасывать applyPhase или planRecord в backToReview.
	for (const phase of ['failed', 'cancelled', 'recovery_required'] as const) {
		it(`после «${phase}» есть «Закрыть» и «Назад к проверке», повтор идёт с новым планом`, async () => {
			vi.mocked(api.getWizardJob).mockResolvedValue({ id: 'job-1', phase, progress: 40, current_step: '', result_available: false } as unknown as Job);
			const { onclose } = mount();
			await toReview();
			await fireEvent.click(button(m.common_apply())!);
			await waitFor(() => expect(button(m.servers_ext_wiz_back_to_review())).not.toBeNull());
			expect(button(m.servers_wizard_apply_cancel())).toBeNull();

			let resolvePreflight: (r: PreflightResponse) => void = () => {};
			vi.mocked(api.runWizardPreflight).mockReturnValueOnce(new Promise((r) => (resolvePreflight = r)));
			await fireEvent.click(button(m.servers_ext_wiz_back_to_review())!);
			expect(button(m.common_apply())?.disabled).toBe(true); // старый план сброшен сразу
			resolvePreflight(PREFLIGHT_OK);
			await waitFor(() => expect(button(m.common_apply())?.disabled).toBe(false));
			expect(button(m.common_back())).not.toBeNull();
			await fireEvent.click(button(m.common_apply())!);
			await waitFor(() => expect(api.applyWizardPlan).toHaveBeenLastCalledWith('tgwebproxy', 'plan-2'));

			await waitFor(() => expect(button(m.servers_ext_wiz_back_to_review())).not.toBeNull());
			const close = screen.getAllByRole('button', { name: m.common_close() }).filter((b) => !b.classList.contains('modal-close'));
			expect(close).toHaveLength(1);
			await fireEvent.click(close[0]);
			expect(onclose).toHaveBeenCalled();
		});
	}

	// Мутация: не сбрасывать preflight/planRecord в начале prepareStep5Plan.
	it('сбой проверки не оставляет прежний план', async () => {
		mount();
		await toReview();
		await fireEvent.click(button(m.common_back())!);
		vi.mocked(api.runWizardPreflight).mockRejectedValueOnce(new Error('сеть'));
		await next();
		await waitFor(() => expect(api.runWizardPreflight).toHaveBeenCalledTimes(2));
		await waitFor(() => expect(button(m.proxy_wizard_next())?.disabled).toBe(false));
		expect(button(m.common_apply())).toBeNull(); // остались на прежнем шаге
		await next();
		await waitFor(() => expect(button(m.common_apply())?.disabled).toBe(false));
		await fireEvent.click(button(m.common_apply())!);
		await waitFor(() => expect(api.applyWizardPlan).toHaveBeenCalledWith('tgwebproxy', 'plan-2'));
	});

	// Мутации: убрать $effect или сравнение planKey; не сбрасывать план в начале prepareStep5Plan.
	it('черновик изменился под планом — план строится заново, при сбое «Применить» недоступна', async () => {
		let resolveNet: (s: XrayRealityState) => void = () => {};
		vi.mocked(api.getXrayReality).mockImplementation((refresh) =>
			refresh ? new Promise((r) => (resolveNet = r)) : Promise.resolve(null as unknown as XrayRealityState)
		);
		mount(
			tgConfig({ scenario: 'dual', direct_host: '', public_hostname: 'tg.example.com' }),
			{ connection_way: 'direct', origin_type: 'public_address' } as XrayConfig
		);
		await toReview();
		expect(vi.mocked(api.createWizardPlan).mock.calls[0][1].direct_host).toBe('tg.example.com');

		let rejectPreflight: (e: Error) => void = () => {};
		vi.mocked(api.runWizardPreflight).mockReturnValueOnce(new Promise((_, r) => (rejectPreflight = r)));
		resolveNet({
			settings: { enabled: false }, network: { public_host: '', white_ip: true, gray_ip: false, external_ip: '198.51.100.10' },
		} as unknown as XrayRealityState);
		await waitFor(() => expect(api.runWizardPreflight).toHaveBeenCalledTimes(2));
		expect(button(m.common_apply())?.disabled).toBe(true);
		rejectPreflight(new Error('сеть'));
		await waitFor(() => expect(button(m.common_back())?.disabled).toBe(false));
		expect(button(m.common_apply())?.disabled).toBe(true);

		await fireEvent.click(button(m.common_back())!);
		await next();
		await waitFor(() => expect(api.createWizardPlan).toHaveBeenCalledTimes(2));
		expect(vi.mocked(api.createWizardPlan).mock.calls[1][1].direct_host).toBe('198.51.100.10');
		await waitFor(() => expect(button(m.common_apply())?.disabled).toBe(false));
		await fireEvent.click(button(m.common_apply())!);
		await waitFor(() => expect(api.applyWizardPlan).toHaveBeenCalledWith('tgwebproxy', 'plan-2'));
	});

	// Мутация: показывать ошибку порта только внутри «Дополнительно».
	it('ошибка порта видна при свёрнутом «Дополнительно»', async () => {
		mount(tgConfig({ scenario: 'dual', direct_port: 443, public_hostname: 'tg.example.com' }));
		await next();
		await next();
		const alert = await screen.findByRole('alert');
		expect(alert.textContent).toContain(m.servers_ext_wiz_s3_direct_port_invalid());
		expect(button(m.proxy_wizard_next())?.disabled).toBe(true);
	});
});
