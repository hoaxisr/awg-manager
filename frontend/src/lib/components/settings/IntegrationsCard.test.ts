import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, within } from '@testing-library/svelte';
import IntegrationsCard from './IntegrationsCard.svelte';
import type { HydraRouteStatus, SingboxStatus } from '$lib/types';

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

describe('IntegrationsCard — HydraRoute', () => {
	const hydra = (installed: boolean): HydraRouteStatus => ({ installed, running: false });

	// Своего установщика HydraRoute у нас нет — кнопка ведёт на инструкцию
	// проекта. Подписывать её «Установить», как у остальных интеграций, значит
	// обещать установку и открыть вместо неё GitHub.
	it('не установлен — ссылка на инструкцию, а не кнопка установки', () => {
		render(IntegrationsCard, {
			...baseProps,
			singboxStatus: status(true),
			showHydra: true,
			hydraStatus: hydra(false),
		});

		const link = screen.getByText('Инструкция →').closest('a');
		expect(link).not.toBeNull();
		expect(link?.getAttribute('href')).toBe('https://github.com/Ground-Zerro/HydraRoute');
		expect(link?.getAttribute('target')).toBe('_blank');
		expect(link?.getAttribute('rel')).toContain('noopener');
	});

	it('установлен — «Открыть» вместо инструкции', () => {
		// sing-box держим неустановленным: у установленного своя кнопка
		// «Открыть», и по тексту их потом не различить.
		render(IntegrationsCard, {
			...baseProps,
			singboxStatus: status(false),
			showHydra: true,
			hydraStatus: hydra(true),
		});
		expect(screen.queryByText('Инструкция →')).toBeNull();

		const open = screen.getByText('Открыть').closest('a');
		expect(open?.getAttribute('href')).toBe('/routing?tab=hrneo');
	});
});

describe('IntegrationsCard — ширина кнопок установки', () => {
	// Пол ширины (min-width: 7.5rem) вешается на прямого ребёнка .setting-row
	// и на ОДИНОЧНУЮ кнопку в .integration-actions. У sing-box кнопка — прямой
	// ребёнок строки, у прокси-бинарей она обёрнута в группу, и пока пол не
	// доставал до вложенных, «Установить» у sing-box был шире остальных.
	// Тест держит структуру, на которую опирается селектор :only-child:
	// появится вторая кнопка в ветке «не установлен» — пол молча отвалится.
	const binary = (key: string, label: string) => ({
		key,
		label,
		present: false,
		installAvailable: true,
		updateAvailable: false,
		busy: false,
		instances: 0,
		oninstall: vi.fn(),
		onuninstall: vi.fn(),
	});

	it('у неустановленного прокси-бинаря «Установить» — единственная кнопка в группе', () => {
		render(IntegrationsCard, {
			...baseProps,
			singboxStatus: status(false),
			// eslint-disable-next-line @typescript-eslint/no-explicit-any
			proxyBinaries: [binary('wdtt', 'WDTT'), binary('freeturn', 'FreeTurn')] as any,
		});

		const groups = document.querySelectorAll('.integration-actions');
		expect(groups.length).toBe(2);
		for (const g of groups) {
			const buttons = g.querySelectorAll('.btn');
			expect(buttons.length).toBe(1);
			expect(buttons[0].textContent?.trim()).toBe('Установить');
		}
	});

	it('sing-box без установки держит кнопку прямым ребёнком строки', () => {
		render(IntegrationsCard, { ...baseProps, singboxStatus: status(false) });

		const row = document.querySelector('.setting-row');
		expect(row).not.toBeNull();
		const direct = row?.querySelector(':scope > .btn');
		expect(direct?.textContent?.trim()).toBe('Установить');
	});
});
