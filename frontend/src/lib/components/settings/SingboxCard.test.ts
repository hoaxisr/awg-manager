import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, within } from '@testing-library/svelte';
import SingboxCard from './SingboxCard.svelte';
import type { SingboxStatus } from '$lib/types';

const status = (installed: boolean): SingboxStatus =>
	({ installed, running: false, version: '1.14.0' }) as SingboxStatus;

const baseProps = {
	singboxInstalling: false,
	singboxInstallError: null,
	oninstallSingbox: vi.fn(),
};

describe('SingboxCard — удаление sing-box', () => {
	it('не установлен — предлагается установка, удалять нечего', () => {
		render(SingboxCard, {
			...baseProps,
			singboxStatus: status(false),
			onuninstallSingbox: vi.fn(),
		});
		expect(screen.queryByText('Установить')).not.toBeNull();
		expect(screen.queryByText('Удалить')).toBeNull();
	});

	it('установлен — кнопка удаления рядом с «Открыть»', () => {
		render(SingboxCard, {
			...baseProps,
			singboxStatus: status(true),
			onuninstallSingbox: vi.fn(),
		});
		expect(screen.queryByText('Открыть')).not.toBeNull();
		expect(screen.queryByText('Удалить')).not.toBeNull();
	});

	it('удаление идёт только после подтверждения', async () => {
		const onuninstallSingbox = vi.fn();
		render(SingboxCard, { ...baseProps, singboxStatus: status(true), onuninstallSingbox });

		await fireEvent.click(screen.getByText('Удалить'));
		expect(onuninstallSingbox).not.toHaveBeenCalled();
		expect(screen.queryByText('Удалить sing-box?')).not.toBeNull();

		await fireEvent.click(within(screen.getByRole('dialog')).getByText('Удалить'));
		expect(onuninstallSingbox).toHaveBeenCalledTimes(1);
	});

	it('без обработчика удаления кнопки нет (старый вызов карточки)', () => {
		render(SingboxCard, { ...baseProps, singboxStatus: status(true) });
		expect(screen.queryByText('Удалить')).toBeNull();
	});
});

describe('SingboxCard — процесс', () => {
	it('работает — перезапуск и остановка идут наверх', async () => {
		const oncontrol = vi.fn();
		render(SingboxCard, { ...baseProps, singboxStatus: { ...status(true), running: true } as SingboxStatus, oncontrol });
		await fireEvent.click(screen.getByText('Перезапустить'));
		await fireEvent.click(screen.getByText('Остановить'));
		expect(oncontrol.mock.calls).toEqual([['restart'], ['stop']]);
	});

	it('ждёт обновления — перезапуск заперт', () => {
		render(SingboxCard, {
			...baseProps,
			singboxStatus: { ...status(true), running: true, updateAvailable: true, requiredVersion: '1.15.0' } as SingboxStatus,
			oncontrol: vi.fn(),
		});
		expect(screen.getByRole<HTMLButtonElement>('button', { name: 'Перезапустить' }).disabled).toBe(true);
	});

	it('не установлен — строки процесса нет', () => {
		render(SingboxCard, { ...baseProps, singboxStatus: status(false), oncontrol: vi.fn() });
		expect(screen.queryByText('Процесс')).toBeNull();
	});
});
