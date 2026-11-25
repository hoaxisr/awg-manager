import { describe, it, expect } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import Host from './PageStateTestHost.svelte';

const icmp = () => screen.getAllByRole<HTMLInputElement>('textbox')[0];

// Смонтирована только активная вкладка — её формы снимаются при переключении.
describe('черновики страницы настроек', () => {
	it('несохранённый ввод переживает уход со вкладки и возврат', async () => {
		const { rerender } = render(Host, { props: { shown: true, target: '8.8.8.8' } });
		await fireEvent.input(icmp(), { target: { value: '1.1.1.1' } });
		await rerender({ shown: false, target: '8.8.8.8' });
		expect(screen.queryByRole('textbox')).toBeNull();
		await rerender({ shown: true, target: '8.8.8.8' });
		expect(icmp().value).toBe('1.1.1.1');
	});

	it('сохранённый черновик забыт: дальше поле следует за настройкой', async () => {
		const { rerender } = render(Host, { props: { shown: true, target: '8.8.8.8' } });
		await fireEvent.input(icmp(), { target: { value: '1.1.1.1' } });
		await rerender({ shown: true, target: '1.1.1.1' });
		await rerender({ shown: true, target: '9.9.9.9' });
		expect(icmp().value).toBe('9.9.9.9');
	});

	it('черновик живёт столько же, сколько страница', async () => {
		const first = render(Host, { props: { shown: true, target: '8.8.8.8' } });
		await fireEvent.input(icmp(), { target: { value: '1.1.1.1' } });
		first.unmount();
		render(Host, { props: { shown: true, target: '8.8.8.8' } });
		expect(icmp().value).toBe('8.8.8.8');
	});
});
