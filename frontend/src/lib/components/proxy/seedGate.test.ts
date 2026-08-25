import { describe, it, expect } from 'vitest';
import { seedGateWarning } from './seedGate';

describe('seedGateWarning: запертый гейт посева обязан быть виден', () => {
	it('посев подтверждён — говорить не о чем', () => {
		expect(seedGateWarning({ seeded: true, certified: true })).toBe('');
	});

	it('поднялась, но не подтверждена — предупреждение о заблокированной уборке', () => {
		expect(seedGateWarning({ seeded: true, certified: false })).toBe(
			'Посев прокси-подсистемы не подтверждён: уборка осиротевших интерфейсов и маршрутов заблокирована.',
		);
	});

	it('причина от бэкенда дописывается к предупреждению', () => {
		expect(seedGateWarning({ seeded: true, certified: false, error: 'реестр выходов недоступен' })).toBe(
			'Посев прокси-подсистемы не подтверждён: уборка осиротевших интерфейсов и маршрутов заблокирована. реестр выходов недоступен',
		);
	});

	it('несостоявшийся посев молчит: про него уже сказал отказ загрузки', () => {
		expect(seedGateWarning({ seeded: false, certified: false, error: 'RCI недоступен' })).toBe('');
	});

	it('пропущенный старый конфиг назван по имени и с причиной', () => {
		expect(
			seedGateWarning({
				seeded: true,
				certified: false,
				error: 'пропущен неразобранный старый конфиг — wdtt.json: поле не того типа',
				skipped: [{ file: 'wdtt.json', reason: 'поле не того типа' }],
			}),
		).toBe(
			'Посев прокси-подсистемы не подтверждён: уборка осиротевших интерфейсов и маршрутов заблокирована. ' +
				'старый конфиг wdtt.json не разобран, его инстансы не перенесены: поле не того типа',
		);
	});

	it('пропущены оба источника — назван каждый', () => {
		expect(
			seedGateWarning({
				seeded: true,
				certified: false,
				skipped: [{ file: 'wdtt.json', reason: 'a' }, { file: 'freeturn.json' }],
			}),
		).toBe(
			'Посев прокси-подсистемы не подтверждён: уборка осиротевших интерфейсов и маршрутов заблокирована. ' +
				'старый конфиг wdtt.json не разобран, его инстансы не перенесены: a; ' +
				'старый конфиг freeturn.json не разобран, его инстансы не перенесены',
		);
	});

	it('состояния ещё нет — предупреждения нет', () => {
		expect(seedGateWarning(null)).toBe('');
	});
});
