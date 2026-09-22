import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import { tick } from 'svelte';
import Modal from './Modal.svelte';

// Слушатель Esc у Modal висит на window и живёт всё время жизни компонента,
// а не пока окно открыто. Значит один Esc физически приходит в КАЖДУЮ
// смонтированную модалку, и отсеять лишние может только сам обработчик.
describe('Modal: Esc', () => {
	it('закрывает только верхнюю из двух открытых модалок', async () => {
		const closeLower = vi.fn();
		const closeUpper = vi.fn();

		// Мастер открыт первым, пикер — поверх него; Esc принадлежит пикеру.
		render(Modal, { props: { open: true, title: 'Мастер', onclose: closeLower } });
		render(Modal, { props: { open: true, title: 'Пикер', onclose: closeUpper } });
		await tick();

		await fireEvent.keyDown(window, { key: 'Escape' });

		expect(closeUpper).toHaveBeenCalledTimes(1);
		expect(closeLower).not.toHaveBeenCalled();
	});

	it('не трогает смонтированную, но закрытую модалку', async () => {
		const closeHidden = vi.fn();
		const closeVisible = vi.fn();

		render(Modal, { props: { open: false, title: 'Скрытая', onclose: closeHidden } });
		render(Modal, { props: { open: true, title: 'Видимая', onclose: closeVisible } });
		await tick();

		await fireEvent.keyDown(window, { key: 'Escape' });

		expect(closeVisible).toHaveBeenCalledTimes(1);
		expect(closeHidden).not.toHaveBeenCalled();
	});

	it('после закрытия верхней Esc снова достаётся нижней', async () => {
		const closeLower = vi.fn();

		render(Modal, { props: { open: true, title: 'Мастер', onclose: closeLower } });
		const upper = render(Modal, { props: { open: true, title: 'Пикер', onclose: vi.fn() } });
		await tick();

		upper.unmount();
		await tick();
		await fireEvent.keyDown(window, { key: 'Escape' });

		expect(closeLower).toHaveBeenCalledTimes(1);
	});
});
