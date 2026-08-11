import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import ProxyInstanceStatusBar from './ProxyInstanceStatusBar.svelte';

describe('ProxyInstanceStatusBar', () => {
	it('остановленный инстанс предлагает только запуск', () => {
		render(ProxyInstanceStatusBar, { running: false, enabled: false, onToggle: vi.fn() });
		expect(screen.getByText('Остановлен')).toBeTruthy();
		expect(screen.getByRole('button', { name: /^Запустить$/ })).toBeTruthy();
		expect(screen.queryByRole('button', { name: /^Остановить$/ })).toBeNull();
	});

	// Регрессия: пока процесс падает при старте, супервизор поднимает инстанс
	// каждые 30 с — а снять флаг «должен работать» было нечем, кнопки не было.
	it('упавший, но включённый инстанс можно остановить и запустить заново', async () => {
		const onToggle = vi.fn();
		render(ProxyInstanceStatusBar, { running: false, enabled: true, onToggle });
		expect(screen.getByText('Не запускается')).toBeTruthy();

		await fireEvent.click(screen.getByRole('button', { name: /^Остановить$/ }));
		expect(onToggle).toHaveBeenCalledWith(false);

		await fireEvent.click(screen.getByRole('button', { name: /^Запустить$/ }));
		expect(onToggle).toHaveBeenCalledWith(true);
	});

	it('работающий инстанс предлагает только остановку', () => {
		render(ProxyInstanceStatusBar, { running: true, enabled: true, onToggle: vi.fn() });
		expect(screen.getByText('Запущен')).toBeTruthy();
		expect(screen.getByRole('button', { name: /^Остановить$/ })).toBeTruthy();
		expect(screen.queryByRole('button', { name: /^Запустить$/ })).toBeNull();
	});
});
