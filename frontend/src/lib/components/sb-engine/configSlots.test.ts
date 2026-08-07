import { describe, it, expect } from 'vitest';
import { slotChips } from './configSlots';
import type { ConfigSlotInfo } from '$lib/types';

function slot(patch: Partial<ConfigSlotInfo> = {}): ConfigSlotInfo {
	return {
		slot: 'tproxy',
		filename: '20-tproxy.json',
		ownership: 'system',
		enabled: true,
		hasDraft: false,
		size: 1024,
		...patch,
	};
}

describe('slotChips', () => {
	it('имя чипа — файл слота без расширения', () => {
		expect(slotChips([slot()])[0].label).toBe('20-tproxy');
	});

	it('порядок ответа сохраняется — это порядок слияния файлов', () => {
		const chips = slotChips([
			slot({ slot: 'base', filename: '00-base.json' }),
			slot({ slot: 'tproxy', filename: '20-tproxy.json' }),
			slot({ slot: 'routing', filename: '21-routing.json' }),
		]);
		expect(chips.map((c) => c.label)).toEqual(['00-base', '20-tproxy', '21-routing']);
	});

	// Ручка отдаёт ВЕСЬ закрытый набор слотов, включая те, файла которых нет.
	// Чип для такого слота обещал бы фрагмент конфига, которого не существует —
	// а режимные слоты взаимоисключающи, и в tproxy их было бы сразу три.
	it('несконфигурированные слоты чипов не получают', () => {
		const chips = slotChips([
			slot({ slot: 'tproxy', filename: '20-tproxy.json', size: 900 }),
			slot({ slot: 'fakeip', filename: '20-fakeip.json', size: 0 }),
			slot({ slot: 'policytun', filename: '20-policytun.json', size: 0 }),
		]);
		expect(chips.map((c) => c.slot)).toEqual(['tproxy']);
	});

	it('выключенный слот помечен состоянием и припиской', () => {
		const [chip] = slotChips([slot({ enabled: false })]);
		expect(chip.state).toBe('off');
		expect(chip.note).toBe('выкл');
	});

	it('слот с черновиком помечен состоянием и припиской', () => {
		const [chip] = slotChips([slot({ hasDraft: true })]);
		expect(chip.state).toBe('draft');
		expect(chip.note).toBe('черновик');
	});

	// «Выключен» важнее: выключенный слот не участвует в сборке вообще, и его
	// черновик — вопрос второго порядка.
	it('выключённый слот с черновиком остаётся выключенным, но говорит про оба', () => {
		const [chip] = slotChips([slot({ enabled: false, hasDraft: true })]);
		expect(chip.state).toBe('off');
		expect(chip.note).toBe('выкл · черновик');
	});

	it('обычный слот приписки не получает', () => {
		const [chip] = slotChips([slot()]);
		expect(chip.state).toBe('active');
		expect(chip.note).toBe('');
	});

	it('подсказка различает пользовательский слот и генерируемый', () => {
		const [user] = slotChips([slot({ slot: 'user', filename: '90-user.json', ownership: 'user' })]);
		const [system] = slotChips([slot()]);
		expect(user.title).toContain('пользовательский');
		expect(system.title).toContain('автоматически');
	});

	it('пустой и отсутствующий ответ дают пустой список', () => {
		expect(slotChips([])).toEqual([]);
		expect(slotChips(null)).toEqual([]);
	});
});
