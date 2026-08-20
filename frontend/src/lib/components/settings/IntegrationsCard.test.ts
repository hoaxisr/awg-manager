import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, within } from '@testing-library/svelte';
import IntegrationsCard from './IntegrationsCard.svelte';
import type { SingboxStatus } from '$lib/types';

const status = (installed: boolean): SingboxStatus =>
	({ installed, running: false, version: '1.14.0' }) as SingboxStatus;

const baseProps = {
	hydraStatus: null,
	singboxInstalling: false,
	singboxInstallError: null,
	oninstallSingbox: vi.fn(),
	showHydra: false,
};

describe('IntegrationsCard — удаление sing-box', () => {
	it('не установлен — предлагается установка, удалять нечего', () => {
		render(IntegrationsCard, {
			...baseProps,
			singboxStatus: status(false),
			onuninstallSingbox: vi.fn(),
		});
		expect(screen.queryByText('Установить')).not.toBeNull();
		expect(screen.queryByText('Удалить')).toBeNull();
	});

	it('установлен — кнопка удаления рядом с «Открыть»', () => {
		render(IntegrationsCard, {
			...baseProps,
			singboxStatus: status(true),
			onuninstallSingbox: vi.fn(),
		});
		expect(screen.queryByText('Открыть')).not.toBeNull();
		expect(screen.queryByText('Удалить')).not.toBeNull();
	});

	it('удаление идёт только после подтверждения', async () => {
		const onuninstallSingbox = vi.fn();
		render(IntegrationsCard, { ...baseProps, singboxStatus: status(true), onuninstallSingbox });

		await fireEvent.click(screen.getByText('Удалить'));
		expect(onuninstallSingbox).not.toHaveBeenCalled();
		expect(screen.queryByText('Удалить sing-box?')).not.toBeNull();

		await fireEvent.click(within(screen.getByRole('dialog')).getByText('Удалить'));
		expect(onuninstallSingbox).toHaveBeenCalledTimes(1);
	});

	it('без обработчика удаления кнопки нет (старый вызов карточки)', () => {
		render(IntegrationsCard, { ...baseProps, singboxStatus: status(true) });
		expect(screen.queryByText('Удалить')).toBeNull();
	});
});
