// Проп target совпадает с опцией монтирования Svelte — пропсы только через { props }.
import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import PingTargetsCard from './PingTargetsCard.svelte';

describe('PingTargetsCard', () => {
	it('сохраняет отредактированные цели, не трогая пропсы', async () => {
		const onsave = vi.fn();
		render(PingTargetsCard, { props: { target: '8.8.8.8', url: 'http://a/204', onsave } });
		const [icmp, http] = screen.getAllByRole<HTMLInputElement>('textbox');
		expect(icmp.value).toBe('8.8.8.8');
		await fireEvent.input(icmp, { target: { value: '1.1.1.1' } });
		await fireEvent.click(screen.getByRole('button', { name: /сохранить/i }));
		expect(onsave).toHaveBeenCalledWith('1.1.1.1', 'http://a/204');
		expect(http.value).toBe('http://a/204');
	});

	it('черновик следует за сохранённым значением', async () => {
		const { rerender } = render(PingTargetsCard, { props: { target: '8.8.8.8', url: '', onsave: vi.fn() } });
		await rerender({ target: '9.9.9.9', url: '', onsave: vi.fn() });
		expect((screen.getAllByRole('textbox')[0] as HTMLInputElement).value).toBe('9.9.9.9');
	});
});
