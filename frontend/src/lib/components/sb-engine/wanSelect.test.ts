import { describe, it, expect } from 'vitest';
import { WAN_AUTO, planWanSelection, wanSelectOptions, wanSelectValue } from './wanSelect';
import type { SingboxRouterSettings, SingboxRouterWANInterface } from '$lib/types';

function cfg(patch: Partial<SingboxRouterSettings> = {}): SingboxRouterSettings {
	return { routingMode: 'tproxy', snifferEnabled: false, ...patch } as SingboxRouterSettings;
}

const ppp: SingboxRouterWANInterface = {
	name: 'ppp0',
	id: 'PPPoE0',
	label: 'Letai (PPPoE)',
	up: true,
	priority: 100,
};
const eth: SingboxRouterWANInterface = {
	name: 'eth3',
	id: 'ISP',
	label: '',
	up: false,
	priority: 200,
};

describe('wanSelectValue', () => {
	it('авто-определение — пункт «Автоматически»', () => {
		expect(wanSelectValue(cfg({ wanAutoDetect: true, wanInterface: '' }))).toBe(WAN_AUTO);
	});

	it('закреплённый интерфейс — его имя', () => {
		expect(wanSelectValue(cfg({ wanAutoDetect: false, wanInterface: 'ppp0' }))).toBe('ppp0');
	});

	// Дефолт бэкенда: WANAutoDetect отсутствует в легаси-payload'ах.
	it('без поля — авто', () => {
		expect(wanSelectValue(cfg())).toBe(WAN_AUTO);
		expect(wanSelectValue(null)).toBe(WAN_AUTO);
	});

	// Пара (auto=false, iface='') невалидна для бэкенда и существовать не
	// может; показываем её как авто, а не как пустой выбор.
	it('невалидную пару показывает как авто', () => {
		expect(wanSelectValue(cfg({ wanAutoDetect: false, wanInterface: '' }))).toBe(WAN_AUTO);
	});
});

describe('wanSelectOptions', () => {
	it('первый пункт — «Автоматически», дальше интерфейсы', () => {
		const out = wanSelectOptions([ppp, eth], WAN_AUTO);
		expect(out.map((o) => o.value)).toEqual([WAN_AUTO, 'ppp0', 'eth3']);
		expect(out[0].label).toBe('Автоматически');
	});

	it('интерфейс с подписью показывает «имя — подпись», без подписи — только имя', () => {
		const out = wanSelectOptions([ppp, eth], WAN_AUTO);
		expect(out[1].label).toBe('ppp0 — Letai (PPPoE)');
		expect(out[2].label).toBe('eth3');
	});

	it('пустой список оставляет только «Автоматически»', () => {
		expect(wanSelectOptions([], WAN_AUTO).map((o) => o.value)).toEqual([WAN_AUTO]);
	});

	// ГЛАВНОЕ: список не загрузился или интерфейс исчез из системы. Без своего
	// пункта Dropdown показал бы плейсхолдер, а следующий выбор молча стёр бы
	// сохранённое закрепление.
	it('сохранённый интерфейс вне списка остаётся выбираемым пунктом', () => {
		const out = wanSelectOptions([], 'ppp0');
		expect(out.map((o) => o.value)).toEqual([WAN_AUTO, 'ppp0']);
		expect(out[1].label).toContain('ppp0');
	});

	it('сохранённый интерфейс из списка не задваивается', () => {
		const out = wanSelectOptions([ppp, eth], 'ppp0');
		expect(out.filter((o) => o.value === 'ppp0')).toHaveLength(1);
		expect(out[1].label).toBe('ppp0 — Letai (PPPoE)');
	});

	it('«Автоматически» лишним пунктом себя не дублирует', () => {
		expect(wanSelectOptions([ppp], WAN_AUTO).filter((o) => o.value === WAN_AUTO)).toHaveLength(1);
	});
});

// Пара (auto, iface) у бэкенда жёстко связана: NormalizeSingboxRouterSettings
// требует auto=true && iface=='' либо auto=false && iface!=''. Один пункт
// списка = одно валидное сохранение, невалидного состояния не существует.
describe('planWanSelection', () => {
	it('«Автоматически» снимает закрепление и чистит интерфейс', () => {
		expect(planWanSelection(WAN_AUTO, 'ppp0')).toEqual({ wanAutoDetect: true, wanInterface: '' });
	});

	it('интерфейс закрепляет пару целиком', () => {
		expect(planWanSelection('ppp0', WAN_AUTO)).toEqual({
			wanAutoDetect: false,
			wanInterface: 'ppp0',
		});
	});

	it('смена одного закреплённого интерфейса на другой', () => {
		expect(planWanSelection('eth3', 'ppp0')).toEqual({
			wanAutoDetect: false,
			wanInterface: 'eth3',
		});
	});

	// Dropdown зовёт onchange и на повторном выборе того же пункта; лишний
	// PUT тянет за собой GET + reload всего конфига движка.
	it('выбор текущего значения ничего не сохраняет', () => {
		expect(planWanSelection(WAN_AUTO, WAN_AUTO)).toBeNull();
		expect(planWanSelection('ppp0', 'ppp0')).toBeNull();
	});
});
