import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
const { status, install, upgrade, uninstall, updateSettings } = vi.hoisted(() => ({
	status: vi.fn(),
	install: vi.fn(),
	upgrade: vi.fn(),
	uninstall: vi.fn(),
	updateSettings: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({
	api: {
		fircGetInstallStatus: status,
		fircInstall: install,
		fircUpgrade: upgrade,
		fircUninstall: uninstall,
		fircUpdateSettings: updateSettings,
	},
}));
import FircIntegrationSection from './FircIntegrationSection.svelte';
import { m } from '$lib/i18n';
import type { FircInstallStatus } from '$lib/types/firc';

const base: FircInstallStatus = {
	state: 'not_installed', available: true, activeEngine: false, busy: false,
	fircdPresent: false, tunvlessPresent: false, initScriptPresent: false, ndmHookPresent: false, configPresent: false,
};
beforeEach(() => {
	vi.resetAllMocks();
	status.mockResolvedValue(base);
	install.mockResolvedValue({ ok: true });
	upgrade.mockResolvedValue({ ok: true });
	uninstall.mockResolvedValue({ ok: true });
	updateSettings.mockResolvedValue({});
});
afterEach(cleanup);

describe('FIRC runtime settings', () => {
	it('does not offer an unconfigured release', async () => {
		status.mockResolvedValue({ ...base, state: 'unavailable', available: false });
		render(FircIntegrationSection);
		const button = await screen.findByRole('button', { name: /Установить FIRC|Install FIRC/ });
		await waitFor(() => expect(button).toBeDisabled());
		expect(install).not.toHaveBeenCalled();
	});
	it('shows both installed and available versions when update is available and blocks active removal', async () => {
		status.mockResolvedValue({
			...base,
			state: 'running',
			activeEngine: true,
			installedVersion: '0.7.0-1',
			availableVersion: '0.8.0-1',
			updateAvailable: true,
			fircdPresent: true,
			tunvlessPresent: true,
		});
		render(FircIntegrationSection);
		expect(await screen.findByText('0.7.0-1')).toBeInTheDocument();
		expect(screen.getByText(/0\.8\.0-1/)).toBeInTheDocument();
		const updateBtn = screen.getByRole('button', { name: /Обновить FIRC|Update FIRC/ });
		await waitFor(() => expect(updateBtn).toBeEnabled());
		expect(screen.getByRole('button', { name: /Удалить FIRC|Remove FIRC/ })).toBeDisabled();
	});
	it('treats accepted install as in progress, not completed', async () => {
		status.mockResolvedValueOnce(base).mockResolvedValue({ ...base, state: 'installing', busy: true, busyAction: 'install', progress: 0.1 });
		render(FircIntegrationSection);
		const button = await screen.findByRole('button', { name: /Установить FIRC|Install FIRC/ });
		await waitFor(() => expect(button).toBeEnabled());
		await fireEvent.click(button);
		await waitFor(() => expect(install).toHaveBeenCalledTimes(1));
		expect(await screen.findByRole('progressbar')).toBeInTheDocument();
		expect(button).toBeDisabled();
	});
	it('offers repair when state is recovery_required', async () => {
		status
			.mockResolvedValueOnce({ ...base, state: 'recovery_required', fircdPresent: true, error: 'interrupted opkg transaction' })
			.mockResolvedValue({ ...base, state: 'installing', busy: true, busyAction: 'install', progress: 0.5 });
		render(FircIntegrationSection);
		const repairBtn = await screen.findByRole('button', { name: /Восстановить FIRC|Repair FIRC/ });
		expect(screen.getByRole('alert')).toHaveTextContent('interrupted opkg transaction');
		await waitFor(() => expect(repairBtn).toBeEnabled());
		await fireEvent.click(repairBtn);
		await waitFor(() => expect(install).toHaveBeenCalledTimes(1));
	});
	it('blocks removal when dependentServices are active', async () => {
		status.mockResolvedValue({
			...base,
			state: 'installed',
			installedVersion: '0.7.0-1',
			fircdPresent: true,
			tunvlessPresent: true,
			dependentServices: ['keendns-cloud', 'server-routing:awg'],
		});
		render(FircIntegrationSection);
		const removeBtn = await screen.findByRole('button', { name: /Удалить FIRC|Remove FIRC/ });
		expect(removeBtn).toBeDisabled();
		expect(screen.getByText(/keendns-cloud, server-routing:awg/)).toBeInTheDocument();
	});
	it('confirms removal with config preservation notice and invokes fircUninstall(false)', async () => {
		status
			.mockResolvedValueOnce({ ...base, state: 'installed', installedVersion: '0.7.0-1', fircdPresent: true, tunvlessPresent: true })
			.mockResolvedValue({ ...base, state: 'uninstalling', busy: true, busyAction: 'uninstall' });
		render(FircIntegrationSection);
		const removeBtn = await screen.findByRole('button', { name: /Удалить FIRC|Remove FIRC/ });
		await waitFor(() => expect(removeBtn).toBeEnabled());
		await fireEvent.click(removeBtn);
		expect(await screen.findByText(/Настройки сохранятся|Configuration is preserved/)).toBeInTheDocument();
		const buttons = screen.getAllByRole('button', { name: /Удалить FIRC|Remove FIRC/ });
		await fireEvent.click(buttons[buttons.length - 1]);
		await waitFor(() => expect(uninstall).toHaveBeenCalledWith(false));
	});
	it('при стороннем firc показывает одно пояснение без установки, обновления и удаления', async () => {
		status.mockResolvedValue({ ...base, state: 'foreign', available: false, foreign: { package: 'firc', webPort: 666 } });
		render(FircIntegrationSection);
		expect(await screen.findByText(m.firc_foreign_text())).toBeInTheDocument();
		expect(screen.getByText(m.firc_foreign_package({ pkg: 'firc' }))).toBeInTheDocument();
		expect(screen.getByRole('link', { name: m.firc_foreign_open_ui() })).toBeInTheDocument();
		expect(screen.queryAllByRole('button')).toHaveLength(0);
		expect(screen.queryByText(m.firc_runtime_activation_note())).not.toBeInTheDocument();
	});
	it('при стороннем firc и выбранном движке FIRC предлагает только уйти с него', async () => {
		status.mockResolvedValue({ ...base, state: 'foreign', available: false, activeEngine: true, foreign: { package: 'firc' } });
		render(FircIntegrationSection);
		const leave = await screen.findByRole('button', { name: m.firc_foreign_leave() });
		expect(screen.getAllByRole('button')).toEqual([leave]);
		expect(screen.getByText(m.firc_foreign_conflict())).toBeInTheDocument();
		await fireEvent.click(leave);
		await waitFor(() => expect(updateSettings).toHaveBeenCalledWith({ routingEngine: 'sing-box' }));
	});
	it('enables Install FIRC when activeEngine is true, stopped, and state is not_installed', async () => {
		status
			.mockResolvedValueOnce({ ...base, state: 'not_installed', activeEngine: true })
			.mockResolvedValue({ ...base, state: 'installing', activeEngine: true, busy: true, busyAction: 'install', progress: 0.2 });
		render(FircIntegrationSection);
		const installBtn = await screen.findByRole('button', { name: /Установить FIRC|Install FIRC/ });
		await waitFor(() => expect(installBtn).toBeEnabled());
		await fireEvent.click(installBtn);
		await waitFor(() => expect(install).toHaveBeenCalledTimes(1));
	});
	it('enables Repair FIRC while keeping Remove FIRC disabled when activeEngine is true and state is recovery_required', async () => {
		status.mockResolvedValue({
			...base,
			state: 'recovery_required',
			activeEngine: true,
			fircdPresent: true,
		});
		render(FircIntegrationSection);
		const repairBtn = await screen.findByRole('button', { name: /Восстановить FIRC|Repair FIRC/ });
		const removeBtn = screen.getByRole('button', { name: /Удалить FIRC|Remove FIRC/ });
		await waitFor(() => expect(repairBtn).toBeEnabled());
		expect(removeBtn).toBeDisabled();
	});
	it('keeps both Install FIRC and Remove FIRC disabled when activeEngine is true and cleanly installed', async () => {
		status.mockResolvedValue({
			...base,
			state: 'installed',
			activeEngine: true,
			installedVersion: '0.7.0-1',
			fircdPresent: true,
			tunvlessPresent: true,
		});
		render(FircIntegrationSection);
		const installBtn = await screen.findByRole('button', { name: /Установить FIRC|Install FIRC/ });
		const removeBtn = screen.getByRole('button', { name: /Удалить FIRC|Remove FIRC/ });
		await waitFor(() => expect(installBtn).toBeDisabled());
		expect(removeBtn).toBeDisabled();
	});
	it('renders action error in alert without disabling retry', async () => {
		install.mockRejectedValueOnce(new Error('temporary opkg lock'));
		render(FircIntegrationSection);
		const installBtn = await screen.findByRole('button', { name: /Установить FIRC|Install FIRC/ });
		await waitFor(() => expect(installBtn).toBeEnabled());
		await fireEvent.click(installBtn);
		expect(await screen.findByRole('alert')).toHaveTextContent('temporary opkg lock');
		expect(installBtn).toBeEnabled();
		await fireEvent.click(installBtn);
		await waitFor(() => expect(install).toHaveBeenCalledTimes(2));
	});

	it('confirms active runtime upgrade, displays rollback and dependent services notice, and shows updating progress', async () => {
		status
			.mockResolvedValueOnce({
				...base,
				state: 'running',
				activeEngine: true,
				installedVersion: '0.7.0-1',
				availableVersion: '0.8.0-1',
				updateAvailable: true,
				dependentServices: ['awg_server', 'vless_xhttp_server'],
				fircdPresent: true,
				tunvlessPresent: true,
			})
			.mockResolvedValue({
				...base,
				state: 'installing',
				activeEngine: true,
				installedVersion: '0.7.0-1',
				availableVersion: '0.8.0-1',
				updateAvailable: false,
				busy: true,
				busyAction: 'upgrade',
				progress: 0.45,
				fircdPresent: true,
				tunvlessPresent: true,
			});
		render(FircIntegrationSection);
		const updateBtn = await screen.findByRole('button', { name: /Обновить FIRC|Update FIRC/ });
		await waitFor(() => expect(updateBtn).toBeEnabled());
		await fireEvent.click(updateBtn);

		expect(await screen.findByText(/0\.7\.0-1.*0\.8\.0-1/)).toBeInTheDocument();
		expect(screen.getByText(/Dependent services will briefly reconnect.*awg_server, vless_xhttp_server|Зависимые сервисы ненадолго переподключатся.*awg_server, vless_xhttp_server/)).toBeInTheDocument();
		expect(screen.getByText(/rollback|откатится/i)).toBeInTheDocument();

		const buttons = screen.getAllByRole('button', { name: /Обновить FIRC|Update FIRC/ });
		await fireEvent.click(buttons[buttons.length - 1]);
		await waitFor(() => expect(upgrade).toHaveBeenCalledTimes(1));
		expect(install).not.toHaveBeenCalled();
		expect(await screen.findByText(/Обновляется|Updating/)).toBeInTheDocument();
		expect(screen.getByRole('progressbar')).toBeInTheDocument();
	});

	it('does not show duplicate available version or update button when already on latest version', async () => {
		status.mockResolvedValue({
			...base,
			state: 'running',
			activeEngine: true,
			installedVersion: '0.8.0-1',
			availableVersion: '0.8.0-1',
			updateAvailable: false,
			fircdPresent: true,
			tunvlessPresent: true,
		});
		render(FircIntegrationSection);
		expect(await screen.findByText('0.8.0-1')).toBeInTheDocument();
		expect(screen.queryByText(/Доступна:|Available:/)).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: /Обновить FIRC|Update FIRC/ })).not.toBeInTheDocument();
	});

	it('shows available version before initial install when runtime is not installed', async () => {
		status.mockResolvedValue({
			...base,
			state: 'not_installed',
			availableVersion: '0.8.0-1',
			available: true,
		});
		render(FircIntegrationSection);
		expect(await screen.findByText(/0\.8\.0-1/)).toBeInTheDocument();
		expect(screen.getByRole('button', { name: /Установить FIRC|Install FIRC/ })).toBeEnabled();
	});
});
