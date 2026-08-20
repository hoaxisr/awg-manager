import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import NotEnabledScreen from './NotEnabledScreen.svelte';

describe('NotEnabledScreen', () => {
	it('на поддерживаемой прошивке кнопка включает режим', async () => {
		const onEnableRequested = vi.fn();
		render(NotEnabledScreen, { onEnableRequested });
		await fireEvent.click(screen.getByText('Включить FakeIP'));
		expect(onEnableRequested).toHaveBeenCalled();
	});

	// Клик по disabled-кнопке в браузере не доходит до обработчика, а
	// fireEvent диспатчит событие принудительно — поэтому проверяем атрибут,
	// а не вызов. Сам вход в режим дополнительно закрыт в FakeIPTab.
	it('на неподдерживаемой — кнопка заблокирована, причина видна', () => {
		const reason = 'Режим требует интерфейс OpkgTun, которого нет в KeeneticOS 4.x';
		render(NotEnabledScreen, { onEnableRequested: vi.fn(), unavailableReason: reason });
		const btn = screen.getByText('Включить FakeIP').closest('button');
		expect(btn?.disabled).toBe(true);
		expect(btn?.title).toBe(reason);
		expect(screen.queryByText(reason)).not.toBeNull();
	});
});
