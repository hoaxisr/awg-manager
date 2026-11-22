import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
const { status, install, uninstall, migrate } = vi.hoisted(() => ({
	status: vi.fn(),
	install: vi.fn(),
	uninstall: vi.fn(),
	migrate: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({
	api: {
		fircGetInstallStatus: status,
		fircInstall: install,
		fircUninstall: uninstall,
		fircMigrateRuntime: migrate,
	},
}));
import FircIntegrationSection from './FircIntegrationSection.svelte';
import type { FircInstallStatus } from '$lib/types/firc';

const base: FircInstallStatus = {
	state: 'not_installed', available: true, activeEngine: false, busy: false,
	fircdPresent: false, tunvlessPresent: false, initScriptPresent: false, ndmHookPresent: false, configPresent: false,
};
beforeEach(() => {
	vi.resetAllMocks();
	status.mockResolvedValue(base);
	install.mockResolvedValue({ ok: true });
	uninstall.mockResolvedValue({ ok: true });
	migrate.mockResolvedValue({ ok: true });
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
	it('shows installed rather than available version and blocks active removal', async () => {
		status.mockResolvedValue({ ...base, state: 'running', activeEngine: true, installedVersion: '0.7.0-1', availableVersion: '0.8.0-1', fircdPresent: true, tunvlessPresent: true });
		render(FircIntegrationSection);
		expect(await screen.findByText('0.7.0-1')).toBeInTheDocument();
		expect(screen.queryByText('0.8.0-1')).not.toBeInTheDocument();
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
	it('offers ownership migration for legacy bundled installations even when running, while blocking direct removal', async () => {
		status
			.mockResolvedValueOnce({
				...base,
				state: 'running',
				activeEngine: true,
				legacyBundledOwnership: true,
				fircdPresent: true,
				tunvlessPresent: true,
			})
			.mockResolvedValue({ ...base, state: 'installing', busy: true, busyAction: 'migrate_legacy', progress: 0.25 });
		render(FircIntegrationSection);
		const migrateBtn = await screen.findByRole('button', { name: /Перенести в пакет FIRC|Migrate FIRC package/ });
		const removeBtn = screen.getByRole('button', { name: /Удалить FIRC|Remove FIRC/ });
		await waitFor(() => expect(migrateBtn).toBeEnabled());
		expect(removeBtn).toBeDisabled();
		await fireEvent.click(migrateBtn);
		await waitFor(() => expect(migrate).toHaveBeenCalledTimes(1));
		expect(install).not.toHaveBeenCalled();
	});
	it('shows external package notice and disables package mutations when managed via opkg', async () => {
		status.mockResolvedValue({
			...base,
			state: 'installed',
			available: false,
			installedVersion: '0.8.2-1',
			externalPackage: 'firc',
			fircdPresent: true,
			tunvlessPresent: true,
			initScriptPresent: true,
			ndmHookPresent: true,
			configPresent: true,
			dependentServices: ['server-routing:awg'],
		});
		render(FircIntegrationSection);
		expect(await screen.findByText('0.8.2-1')).toBeInTheDocument();
		expect(screen.getByText(/внешний пакет Entware «firc»|external Entware package "firc"/)).toBeInTheDocument();
		expect(screen.getByRole('button', { name: /Установить FIRC|Install FIRC/ })).toBeDisabled();
		expect(screen.getByRole('button', { name: /Удалить FIRC|Remove FIRC/ })).toBeDisabled();
		expect(screen.queryByText(/ещё не опубликован|has not been published/)).not.toBeInTheDocument();
		expect(screen.queryByText(/server-routing:awg/)).not.toBeInTheDocument();
	});
	it('offers confirmed migration from eligible external Entware firc package even when running', async () => {
		status
			.mockResolvedValueOnce({
				...base,
				state: 'running',
				activeEngine: true,
				installedVersion: '0.8.2-1',
				externalPackage: 'firc',
				externalMigratable: true,
				fircdPresent: true,
				tunvlessPresent: true,
				initScriptPresent: true,
				ndmHookPresent: true,
				configPresent: true,
			})
			.mockResolvedValue({ ...base, state: 'installing', busy: true, busyAction: 'migrate_external', progress: 0.4 });
		render(FircIntegrationSection);
		const switchBtn = await screen.findByRole('button', { name: /Перейти на FIRC от AWGM|Switch to AWGM FIRC/ });
		await waitFor(() => expect(switchBtn).toBeEnabled());
		expect(screen.getByRole('button', { name: /Удалить FIRC|Remove FIRC/ })).toBeDisabled();

		await fireEvent.click(switchBtn);
		expect(await screen.findByText(/\/opt\/etc\/firc.*\/opt\/var\/lib\/firc/)).toBeInTheDocument();
		const confirmButtons = screen.getAllByRole('button', { name: /Перейти на FIRC от AWGM|Switch to AWGM FIRC/ });
		await fireEvent.click(confirmButtons[confirmButtons.length - 1]);
		await waitFor(() => expect(migrate).toHaveBeenCalledWith({ replaceExternalPackage: true }));
		expect(install).not.toHaveBeenCalled();
	});
	it('blocks external migration button when dependent services are active', async () => {
		status.mockResolvedValue({
			...base,
			state: 'running',
			activeEngine: true,
			installedVersion: '0.8.2-1',
			externalPackage: 'firc',
			externalMigratable: true,
			fircdPresent: true,
			tunvlessPresent: true,
			dependentServices: ['vless_xhttp_server'],
		});
		render(FircIntegrationSection);
		const switchBtn = await screen.findByRole('button', { name: /Перейти на FIRC от AWGM|Switch to AWGM FIRC/ });
		expect(switchBtn).toBeDisabled();
		expect(screen.getByText(/vless_xhttp_server/)).toBeInTheDocument();
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
});
