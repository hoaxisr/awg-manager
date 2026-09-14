import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent, screen } from '@testing-library/svelte';
import PremiumDeclaredCountryField from './PremiumDeclaredCountryField.svelte';

const amneziaPremiumDeclaredCountry = vi.fn();
const amneziaPremiumSaveDeclaredCountry = vi.fn();

vi.mock('$lib/api/client', () => ({
	api: {
		amneziaPremiumDeclaredCountry: (...a: unknown[]) => amneziaPremiumDeclaredCountry(...a),
		amneziaPremiumSaveDeclaredCountry: (...a: unknown[]) => amneziaPremiumSaveDeclaredCountry(...a)
	}
}));

async function settle() {
	for (let i = 0; i < 4; i++) await Promise.resolve();
	await new Promise((r) => setTimeout(r, 0));
}

beforeEach(() => {
	vi.clearAllMocks();
	amneziaPremiumDeclaredCountry.mockResolvedValue({ declaredCountryCode: '' });
	amneziaPremiumSaveDeclaredCountry.mockResolvedValue({ declaredCountryCode: 'ag' });
});

describe('PremiumDeclaredCountryField', () => {
	it('поднимает сохранённый выбор с роутера', async () => {
		amneziaPremiumDeclaredCountry.mockResolvedValue({ declaredCountryCode: 'ru' });
		const onchange = vi.fn();
		render(PremiumDeclaredCountryField, { props: { value: '', disabled: false, onchange } });
		await settle();

		expect(amneziaPremiumDeclaredCountry).toHaveBeenCalledTimes(1);
		expect(onchange).toHaveBeenCalledWith('ru');
	});

	it('выбор сохраняется на роутере и объявляется мастеру', async () => {
		const onchange = vi.fn();
		render(PremiumDeclaredCountryField, { props: { value: '', disabled: false, onchange } });
		await settle();
		onchange.mockClear();

		await fireEvent.click(screen.getByText('Другие страны и регионы'));
		await settle();

		expect(amneziaPremiumSaveDeclaredCountry).toHaveBeenCalledWith('ag');
		expect(onchange).toHaveBeenCalledWith('ag');
	});

	// Объявить выбор до удавшейся записи значит разблокировать РАСХОДНУЮ
	// кнопку по значению, которого на роутере нет: выдача откажет уже после
	// нажатия, и человек решит, что сломалась она.
	it('неудавшаяся запись выбор не объявляет', async () => {
		amneziaPremiumSaveDeclaredCountry.mockRejectedValue(new Error('роутер занят'));
		const onchange = vi.fn();
		render(PremiumDeclaredCountryField, { props: { value: '', disabled: false, onchange } });
		await settle();
		onchange.mockClear();

		await fireEvent.click(screen.getByText('Россия'));
		await settle();

		expect(onchange).not.toHaveBeenCalled();
		expect(screen.getByText('роутер занят')).toBeTruthy();
	});
});
