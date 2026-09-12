import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import BackupRestoreCard from './BackupRestoreCard.svelte';

vi.mock('$lib/api/client', () => ({
	api: { createBackup: vi.fn(), restoreBackup: vi.fn() }
}));
vi.mock('$lib/stores/notifications', () => ({
	notifications: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }
}));

// Архив резервной копии несёт секреты ОТКРЫТЫМ текстом: ключ API панели,
// приватные и preshared-ключи туннелей и пиров. Его пересылают в поддержку и
// кладут в облако, а шифруется в нём только ключ подписки Amnezia — из-за чего
// возникает ложное впечатление, будто защищён весь архив. Предупреждение
// обязано стоять ДО выгрузки, а не в документации.
describe('BackupRestoreCard: предупреждение о секретах', () => {
	it('называет секреты и говорит хранить архив как пароль', () => {
		render(BackupRestoreCard);

		expect(screen.getByText(/Архив содержит секреты в открытом виде/)).toBeTruthy();
		const text = document.body.textContent ?? '';
		expect(text).toMatch(/ключ API/i);
		// Пробелы гибкие: в разметке текст переносится, и жёсткий пробел ловил бы
		// вёрстку, а не содержание.
		expect(text).toMatch(/приватные\s+и\s+preshared-ключи/i);
		expect(text).toMatch(/как\s+пароль/i);
	});

	it('не выдаёт архив за защищённый: оговорка про ключ подписки на месте', () => {
		render(BackupRestoreCard);
		const text = document.body.textContent ?? '';
		// Шифруется ровно одно поле, и сказать об этом надо честно — иначе
		// упоминание шифрования читается как «архив зашифрован».
		expect(text).toMatch(/только\s+ключ\s+подписки/i);
		expect(text).toMatch(/на\s+другом\s+не\s+прочитается/i);
	});
});
