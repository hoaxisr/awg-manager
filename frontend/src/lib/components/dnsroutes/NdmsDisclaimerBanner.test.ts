import { describe, it, expect, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import NdmsDisclaimerBanner from './NdmsDisclaimerBanner.svelte';

const KEY = 'awgm.ndmsDisclaimerAck';

async function openQuiz(): Promise<void> {
	await fireEvent.click(screen.getByRole('button', { name: /проверить себя/i }));
}

describe('NdmsDisclaimerBanner', () => {
	beforeEach(() => {
		localStorage.clear();
	});

	it('не рендерится вне OS5', () => {
		render(NdmsDisclaimerBanner, { isOS5: false });
		expect(screen.queryByText(/Ограничения DNS-маршрутизации NDMS/i)).toBeNull();
	});

	it('неверный ответ не подтверждает дисклеймер и показывает пояснение', async () => {
		render(NdmsDisclaimerBanner, { isOS5: true });
		await openQuiz();
		await fireEvent.click(
			screen.getByRole('button', { name: /политика на DNS-маршруты не влияет/i }),
		);
		expect(screen.getByText(/только «Политику по умолчанию»/i)).toBeTruthy();
		expect(localStorage.getItem(KEY)).toBeNull();
	});

	it('все верные ответы подтверждают дисклеймер и схлопывают баннер', async () => {
		render(NdmsDisclaimerBanner, { isOS5: true });
		await openQuiz();
		await fireEvent.click(screen.getByRole('button', { name: /^Нет$/ }));
		await fireEvent.click(screen.getByRole('button', { name: /мимо резолвера роутера/i }));
		await fireEvent.click(screen.getByRole('button', { name: /yandex\.com/i }));
		expect(screen.getByText(/Ограничения NDMS-маршрутизации приняты/i)).toBeTruthy();
		expect(localStorage.getItem(KEY)).toBe('1');
	});

	it('при выставленном флаге баннер сразу свёрнут', () => {
		localStorage.setItem(KEY, '1');
		render(NdmsDisclaimerBanner, { isOS5: true });
		expect(screen.getByText(/Ограничения NDMS-маршрутизации приняты/i)).toBeTruthy();
		expect(screen.queryByRole('button', { name: /проверить себя/i })).toBeNull();
	});

	it('«Показать» разворачивает список ограничений без повторного квиза', async () => {
		localStorage.setItem(KEY, '1');
		render(NdmsDisclaimerBanner, { isOS5: true });
		await fireEvent.click(screen.getByRole('button', { name: /Показать/i }));
		expect(screen.getByText(/Политика по умолчанию/i)).toBeTruthy();
		expect(screen.queryByRole('button', { name: /проверить себя/i })).toBeNull();
	});
});
