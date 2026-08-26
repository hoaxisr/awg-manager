import { describe, it, expect } from 'vitest';
import { seedGateWarning, seedListenMoveNotice } from './seedGate';

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

describe('seedListenMoveNotice: переезд listen-порта обязан быть виден', () => {
	it('молчит, когда никто не переезжал', () => {
		expect(seedListenMoveNotice({ seeded: true, certified: true })).toBe('');
		expect(seedListenMoveNotice({ seeded: true, certified: true, movedListen: [] })).toBe('');
		expect(seedListenMoveNotice(null)).toBe('');
	});

	it('называет инстанс и ОБА адреса на заверенном посеве', () => {
		const text = seedListenMoveNotice({
			seeded: true,
			certified: true,
			movedListen: [
				{ instance: 'freeturn-client:default', name: 'Клиент', from: '127.0.0.1:9000', to: '127.0.0.1:9002' },
			],
		});
		expect(text).toContain('«Клиент»');
		expect(text).toContain('127.0.0.1:9000');
		expect(text).toContain('127.0.0.1:9002');
	});

	it('без имени называет инстанс ключом — безымянного тоже надо опознать', () => {
		const text = seedListenMoveNotice({
			seeded: true,
			certified: true,
			movedListen: [{ instance: 'wdtt-client:wc', from: '0.0.0.0:9000', to: '127.0.0.1:9001' }],
		});
		expect(text).toContain('«wdtt-client:wc»');
	});
});
