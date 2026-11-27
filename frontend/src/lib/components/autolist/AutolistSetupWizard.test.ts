import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import AutolistSetupWizard from './AutolistSetupWizard.svelte';
import { m } from '$lib/i18n';
import { api } from '$lib/api/client';
import { notifications } from '$lib/stores/notifications';
import type { AutolistConfig } from '$lib/types';

const baseConfig: AutolistConfig = {
	autoAdd: false,
	targetInterface: '',
	resolveVia: '',
	wanCheckUrl: 'https://ya.ru/robots.txt',
	wanMonitorEnabled: false,
	tunnelProbe: false,
	excludeDomains: [],
	excludeRuleSets: [],
	udpEnabled: false,
	addsPerHour: 50,
};

beforeEach(() => {
	vi.stubGlobal('ResizeObserver', class {
		observe() {}
		disconnect() {}
	});
	vi.spyOn(notifications, 'success').mockImplementation(() => '');
	vi.spyOn(notifications, 'error').mockImplementation(() => '');
	vi.spyOn(notifications, 'warning').mockImplementation(() => '');
	vi.spyOn(api, 'getAutolistConfig').mockResolvedValue(baseConfig);
	vi.spyOn(api, 'setAutolistConfig').mockResolvedValue();
});

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
	vi.unstubAllGlobals();
});

function renderWizard() {
	const onclose = vi.fn();
	const onsaved = vi.fn();
	render(AutolistSetupWizard, {
		props: {
			open: true,
			onclose,
			onsaved,
			onfetchresources: () => {},
			awgTagInfos: [],
			singboxTunnels: [],
			subscriptions: [],
			interfaces: [{ name: 'nwg0', label: 'Home', up: true, securityLevel: 'public' }],
			dnsServers: [],
			dnsGlobals: { final: 'dns-final', strategy: 'prefer_ipv4' },
			exclusionRuleSets: [],
		},
	});
	return { onclose, onsaved };
}

/** Раскрыть dropdown по индексу триггера и кликнуть опцию с подписью. */
async function pickDropdownOption(triggerIndex: number, label: string) {
	const triggers = document.querySelectorAll('button[aria-haspopup="listbox"]');
	await fireEvent.click(triggers[triggerIndex]);
	const panel = document.querySelector('.dropdown-panel') as HTMLElement;
	expect(panel).toBeTruthy();
	const option = Array.from(panel.querySelectorAll('.option')).find(
		(el) => el.querySelector('.option-label')?.textContent === label,
	) as HTMLElement;
	expect(option).toBeTruthy();
	await fireEvent.click(option);
}

const nextButton = () => screen.getByRole('button', { name: m.proxy_wizard_next() });

describe('AutolistSetupWizard', () => {
	it('gates the first step on an explicit tunnel choice', async () => {
		renderWizard();
		await screen.findByText(m.autolist_wizard_step_tunnel());

		expect(nextButton().hasAttribute('disabled')).toBe(true);
		await pickDropdownOption(0, 'Home (nwg0)');
		expect(nextButton().hasAttribute('disabled')).toBe(false);
		// Автоматического effective-туннеля нет: значение — только выбранное.
		expect(api.setAutolistConfig).not.toHaveBeenCalled();
	});

	it('finishes from the mode step in manual mode without risk acknowledgement', async () => {
		const { onsaved, onclose } = renderWizard();
		await screen.findByText(m.autolist_wizard_step_tunnel());
		await pickDropdownOption(0, 'Home (nwg0)');

		// Шаг 2: DNS default = dns.final — просто идём дальше.
		await fireEvent.click(nextButton());
		await screen.findByText(m.autolist_wizard_step_dns());
		await fireEvent.click(nextButton());

		// Шаг 3: ручной режим default — финиш прямо с шага режима.
		await screen.findByText(m.autolist_wizard_step_mode());
		expect(screen.queryByText(m.autolist_wizard_step_risks())).toBeNull();
		await fireEvent.click(screen.getByRole('button', { name: m.autolist_wizard_finish() }));

		await vi.waitFor(() => expect(api.setAutolistConfig).toHaveBeenCalledTimes(1));
		const sent = vi.mocked(api.setAutolistConfig).mock.calls[0][0];
		expect(sent.autoAdd).toBe(false);
		expect(sent.targetInterface).toBe('nwg0');
		// Ручный режим не трогает tunnel-пробу.
		expect(sent.tunnelProbe).toBe(false);
		expect(onsaved).toHaveBeenCalledOnce();
		expect(onclose).toHaveBeenCalledOnce();
	});

	it('requires the risk acknowledgement before AutoAdd can continue', async () => {
		renderWizard();
		await screen.findByText(m.autolist_wizard_step_tunnel());
		await pickDropdownOption(0, 'Home (nwg0)');
		await fireEvent.click(nextButton());
		await fireEvent.click(nextButton());

		await pickDropdownOption(0, m.autolist_wizard_mode_auto());
		expect(screen.getByText(m.autolist_wizard_mode_auto_warning())).toBeTruthy();
		await fireEvent.click(nextButton());

		// Шаг рисков: «Дальше» заблокировано до явного согласия.
		await screen.findByText(m.autolist_wizard_risks_intro());
		const warning = document.querySelector('.autolist-warning');
		expect(warning?.querySelector('.risk-list')).not.toBeNull();
		expect(warning?.querySelector('input[type="checkbox"]')).not.toBeNull();
		expect(nextButton().hasAttribute('disabled')).toBe(true);
		await fireEvent.click(screen.getByRole('checkbox', { name: m.autolist_wizard_risks_ack() }));
		expect(nextButton().hasAttribute('disabled')).toBe(false);
	});

	it('saves AutoAdd with tunnelProbe=true and the entered exclusions', async () => {
		const { onsaved } = renderWizard();
		await screen.findByText(m.autolist_wizard_step_tunnel());
		await pickDropdownOption(0, 'Home (nwg0)');
		await fireEvent.click(nextButton());
		await fireEvent.click(nextButton());
		await pickDropdownOption(0, m.autolist_wizard_mode_auto());
		await fireEvent.click(nextButton());
		await fireEvent.click(screen.getByRole('checkbox', { name: m.autolist_wizard_risks_ack() }));
		await fireEvent.click(nextButton());

		// Шаг исключений: рекомендация заполнить + поле доменов.
		await screen.findByText(m.autolist_wizard_exclusions_hint());
		await fireEvent.input(screen.getByPlaceholderText('example.com, sub.example.org'), { target: { value: 'bank.example' } });
		await fireEvent.click(screen.getByRole('button', { name: m.autolist_wizard_finish() }));

		await vi.waitFor(() => expect(api.setAutolistConfig).toHaveBeenCalledTimes(1));
		const sent = vi.mocked(api.setAutolistConfig).mock.calls[0][0];
		expect(sent.autoAdd).toBe(true);
		expect(sent.tunnelProbe).toBe(true);
		expect(sent.excludeDomains).toEqual(['bank.example']);
		expect(onsaved).toHaveBeenCalledOnce();
	});

	it('finishing without exclusions requires acknowledgement in the inline warning', async () => {
		renderWizard();
		await screen.findByText(m.autolist_wizard_step_tunnel());
		await pickDropdownOption(0, 'Home (nwg0)');
		await fireEvent.click(nextButton());
		await fireEvent.click(nextButton());
		await pickDropdownOption(0, m.autolist_wizard_mode_auto());
		await fireEvent.click(nextButton());
		await fireEvent.click(screen.getByRole('checkbox', { name: m.autolist_wizard_risks_ack() }));
		await fireEvent.click(nextButton());
		await screen.findByText(m.autolist_wizard_exclusions_hint());

		expect(screen.queryByRole('button', { name: 'Пропустить' })).toBeNull();
		const finish = screen.getByRole('button', { name: m.autolist_wizard_finish() });
		await fireEvent.click(finish);
		await screen.findByText(m.autolist_wizard_skip_warning_title());
		expect(api.setAutolistConfig).not.toHaveBeenCalled();
		expect(finish.hasAttribute('disabled')).toBe(true);
		expect(screen.queryByRole('button', { name: 'Всё равно продолжить без исключений' })).toBeNull();

		await fireEvent.click(screen.getByRole('checkbox', { name: m.autolist_wizard_skip_ack() }));
		expect(finish.hasAttribute('disabled')).toBe(false);
		await fireEvent.click(finish);
		await vi.waitFor(() => expect(api.setAutolistConfig).toHaveBeenCalledTimes(1));
		const sent = vi.mocked(api.setAutolistConfig).mock.calls[0][0];
		expect(sent.autoAdd).toBe(true);
		expect(sent.excludeDomains).toEqual([]);
	});
});
