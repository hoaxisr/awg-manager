import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent, screen, waitFor } from '@testing-library/svelte';
import PremiumMirrorField from './PremiumMirrorField.svelte';

const amneziaPremiumMirror = vi.fn();
const amneziaPremiumSaveMirror = vi.fn();

vi.mock('$lib/api/client', () => ({
	api: {
		amneziaPremiumMirror: (...a: unknown[]) => amneziaPremiumMirror(...a),
		amneziaPremiumSaveMirror: (...a: unknown[]) => amneziaPremiumSaveMirror(...a)
	}
}));

// Адреса фикстур различимы между собой и не совпадают ни с каким умолчанием
// продукта: совпадение сделало бы тест слепым к подставленному литералу.
const EFFECTIVE = 'https://mirror-alpha.fixture.test/cp?m-path=/xx';
const AFTER_RESET = 'https://mirror-omega.fixture.test/cp?m-path=/yy';

async function settle() {
	for (let i = 0; i < 4; i++) await Promise.resolve();
	await new Promise((r) => setTimeout(r, 0));
}

beforeEach(() => {
	vi.clearAllMocks();
	amneziaPremiumMirror.mockResolvedValue({ mirrorUrl: EFFECTIVE });
	amneziaPremiumSaveMirror.mockResolvedValue({ mirrorUrl: AFTER_RESET });
});

describe('PremiumMirrorField', () => {
	it('показывает действующий адрес с бэкенда, а не своё умолчание', async () => {
		render(PremiumMirrorField, { props: { forceOpen: true } });
		await settle();

		const field = screen.getByLabelText<HTMLInputElement>('Адрес зеркала Amnezia');
		expect(field.value).toBe(EFFECTIVE);
		expect(amneziaPremiumMirror).toHaveBeenCalledTimes(1);
	});

	it('свёрнутое поле за адресом не ходит, пока его не раскрыли', async () => {
		render(PremiumMirrorField, { props: { forceOpen: false } });
		await settle();

		expect(amneziaPremiumMirror).not.toHaveBeenCalled();
		expect(screen.queryByLabelText('Адрес зеркала Amnezia')).toBeNull();

		await fireEvent.click(screen.getByRole('button', { name: 'Адрес зеркала Amnezia' }));
		await settle();

		expect(amneziaPremiumMirror).toHaveBeenCalledTimes(1);
		expect(
			screen.getByLabelText<HTMLInputElement>('Адрес зеркала Amnezia').value
		).toBe(EFFECTIVE);
	});

	it('«вернуть по умолчанию» шлёт пустое значение и показывает, что вернулось', async () => {
		render(PremiumMirrorField, { props: { forceOpen: true } });
		await settle();

		await fireEvent.click(screen.getByRole('button', { name: 'Вернуть по умолчанию' }));
		await waitFor(() => expect(amneziaPremiumSaveMirror).toHaveBeenCalledTimes(1));

		// Дефолт объявлен на бэкенде: наружу уходит пустое значение.
		expect(amneziaPremiumSaveMirror).toHaveBeenCalledWith('');
		await waitFor(() =>
			expect(screen.getByLabelText<HTMLInputElement>('Адрес зеркала Amnezia').value).toBe(
				AFTER_RESET
			)
		);
	});

	it('отказ на не-https показывается текстом бэкенда', async () => {
		const rejected: Error & { body?: unknown } = new Error(
			'адрес зеркала Amnezia должен быть абсолютным https-адресом'
		);
		rejected.body = { code: 'INVALID_AMNEZIA_MIRROR_URL' };
		amneziaPremiumSaveMirror.mockRejectedValue(rejected);

		render(PremiumMirrorField, { props: { forceOpen: true } });
		await settle();

		const field = screen.getByLabelText('Адрес зеркала Amnezia');
		await fireEvent.input(field, { target: { value: 'http://mirror-beta.fixture.test/cp' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }));
		await settle();

		expect(
			screen.getByText('адрес зеркала Amnezia должен быть абсолютным https-адресом')
		).toBeTruthy();
	});
});
