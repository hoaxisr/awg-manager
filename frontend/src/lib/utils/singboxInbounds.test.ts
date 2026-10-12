import { describe, it, expect } from 'vitest';
import type { SingboxInboundEntry } from '$lib/types';
import {
	groupInbounds,
	inboundListenLabel,
	idleBadgeLabel,
	idleTitle,
	deviceProxyInboundTag,
	inboundsPanelTotal,
	inboundGroupTitle,
} from './singboxInbounds';

function entry(over: Partial<SingboxInboundEntry>): SingboxInboundEntry {
	return {
		tag: 't-in',
		type: 'mixed',
		listen: '127.0.0.1',
		listenPort: 1080,
		slot: 'tunnels',
		source: 'tunnel',
		ownerLabel: '',
		idle: false,
		idleReason: '',
		...over,
	};
}

describe('groupInbounds', () => {
	it('группирует по source в фиксированном порядке, пустые группы опущены', () => {
		const groups = groupInbounds([
			entry({ tag: 'sub-a-in', source: 'subscription' }),
			entry({ tag: 'tun-in', type: 'tun', source: 'engine', listen: '', listenPort: 0 }),
			entry({ tag: 'agg-b-in', source: 'group' }),
			entry({ tag: 'my-vless-in', source: 'tunnel' }),
		]);
		expect(groups.map((g) => g.source)).toEqual(['engine', 'subscription', 'group', 'tunnel']);
		expect(groups[0].title).toBe('Движок');
		expect(groups[1].title).toBe('Подписки');
		expect(groups[2].title).toBe('Сводные группы');
		expect(groups[3].title).toBe('Туннели');
	});

	it('сохраняет порядок записей внутри группы', () => {
		const groups = groupInbounds([
			entry({ tag: 'sub-1-in', source: 'subscription' }),
			entry({ tag: 'sub-2-in', source: 'subscription' }),
		]);
		expect(groups).toHaveLength(1);
		expect(groups[0].entries.map((e) => e.tag)).toEqual(['sub-1-in', 'sub-2-in']);
	});

	it('пустой вход → пустой список групп', () => {
		expect(groupInbounds([])).toEqual([]);
	});

	it('неизвестный source попадает в «Прочее»', () => {
		const groups = groupInbounds([
			entry({ tag: 'x-in', source: 'weird' as SingboxInboundEntry['source'] }),
		]);
		expect(groups).toHaveLength(1);
		expect(groups[0].source).toBe('other');
		expect(groups[0].title).toBe(inboundGroupTitle('other'));
	});
});

describe('inboundListenLabel', () => {
	it('адрес:порт для обычного inbound', () => {
		expect(inboundListenLabel(entry({}))).toBe('127.0.0.1:1080');
	});
	it('tun без listen/port → «—»', () => {
		expect(inboundListenLabel(entry({ listen: '', listenPort: 0, type: 'tun' }))).toBe('—');
	});
	it('порт без адреса → 0.0.0.0:порт', () => {
		expect(inboundListenLabel(entry({ listen: '', listenPort: 1099 }))).toBe('0.0.0.0:1099');
	});
});

describe('idleBadgeLabel / idleTitle', () => {
	it('не-idle → пустые строки', () => {
		expect(idleBadgeLabel(entry({}))).toBe('');
		expect(idleTitle(entry({}))).toBe('');
	});
	it('ndms_proxy_disabled', () => {
		const e = entry({ idle: true, idleReason: 'ndms_proxy_disabled' });
		expect(idleBadgeLabel(e)).toBe('резерв порта — NDMS-прокси выключен');
		expect(idleTitle(e)).toContain('порт остаётся зарезервированным');
	});
	it('no_route_rule', () => {
		const e = entry({ idle: true, idleReason: 'no_route_rule' });
		expect(idleBadgeLabel(e)).toBe(
			'не используется — конфиг не направляет трафик с этого порта',
		);
		expect(idleTitle(e)).toContain('стабильности порта');
	});
	it('ndms_proxy_missing', () => {
		const e = entry({ idle: true, idleReason: 'ndms_proxy_missing' });
		expect(idleBadgeLabel(e)).toBe('NDMS-прокси не создан');
		expect(idleTitle(e)).toContain('ProxyN');
	});
});

describe('deviceProxyInboundTag', () => {
	it('легаси default без id, именованный — с id', () => {
		expect(deviceProxyInboundTag('default')).toBe('device-proxy-in');
		expect(deviceProxyInboundTag('')).toBe('device-proxy-in');
		expect(deviceProxyInboundTag('abc')).toBe('device-proxy-abc-in');
	});
});

describe('inboundsPanelTotal', () => {
	const cfg = [
		entry({ tag: 'tun-in', type: 'tun', source: 'engine', listen: '', listenPort: 0 }),
		entry({ tag: 'device-proxy-abc-in', source: 'deviceproxy', slot: 'deviceproxy' }),
	];
	it('все инстансы в конфиге → счёт = число inbound’ов', () => {
		expect(inboundsPanelTotal(cfg, ['abc'])).toBe(2);
	});
	it('выключенный инстанс отсутствует в конфиге, но виден карточкой → +1', () => {
		expect(inboundsPanelTotal(cfg, ['abc', 'off1'])).toBe(3);
	});
	it('легаси default-инстанс матчится по device-proxy-in', () => {
		const withDefault = [
			...cfg,
			entry({ tag: 'device-proxy-in', source: 'deviceproxy', slot: 'deviceproxy' }),
		];
		expect(inboundsPanelTotal(withDefault, ['abc', 'default'])).toBe(3);
	});
	it('без инстансов → просто длина списка', () => {
		expect(inboundsPanelTotal(cfg, [])).toBe(2);
	});
});
