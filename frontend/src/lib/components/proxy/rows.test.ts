import { describe, it, expect } from 'vitest';
import type {
	WdttClientConfig,
	WdttClientInstance,
	WdttConfig,
	WdttInstanceStatus,
	WdttProcessStatus,
} from '$lib/types';
import { exitRows, rowKeyFromInstanceKey } from './rows';

function instance(id: string, running: boolean, lastError?: string): WdttInstanceStatus {
	const status: WdttProcessStatus = { running, lastError, binary: 'wdtt', binaryPresent: true };
	return { id, name: id, status };
}

function client(id: string, enabled: boolean): WdttClientInstance {
	// Строка читает из конфига только enabled и connMode.
	return { id, name: id, config: { enabled } as unknown as WdttClientConfig };
}

function sources(status: WdttInstanceStatus[], config: WdttClientInstance[]) {
	const dead: WdttProcessStatus = { running: false, binary: '', binaryPresent: false };
	return {
		wdttStatus: {
			clients: status,
			servers: [],
			client: dead,
			server: dead,
			installAvailable: false,
			updateAvailable: false,
			installing: false,
		},
		wdttConfig: { clients: config, servers: [] } as unknown as WdttConfig,
		ftStatus: null,
		ftConfig: null,
	};
}

describe('exitRows: состояние строки', () => {
	it('живой процесс — «Запущен»', () => {
		const rows = exitRows(sources([instance('a', true)], [client('a', true)]));
		expect(rows[0].state).toBe('running');
	});

	it('должен работать, но не работает — «Не запускается»', () => {
		const rows = exitRows(sources([instance('a', false, 'connection refused')], [client('a', true)]));
		expect(rows[0].state).toBe('error');
	});

	it('после явного стопа — «Остановлен», даже если ошибка последнего запуска жива', () => {
		const rows = exitRows(
			sources([instance('a', false, 'connection refused')], [client('a', false)]),
		);
		expect(rows[0].state).toBe('stopped');
	});

	it('остановлен без ошибки — «Остановлен»', () => {
		const rows = exitRows(sources([instance('a', false)], [client('a', false)]));
		expect(rows[0].state).toBe('stopped');
	});
});

describe('rowKeyFromInstanceKey: ключ бэкенда → ключ строки', () => {
	it('клиент wdtt', () => {
		expect(rowKeyFromInstanceKey('wdtt-client:nl')).toEqual({
			key: 'wdtt:client:nl',
			role: 'client',
		});
	});

	it('клиент freeturn', () => {
		expect(rowKeyFromInstanceKey('freeturn-client:default')).toEqual({
			key: 'freeturn:client:default',
			role: 'client',
		});
	});

	it('серверы обоих протоколов', () => {
		expect(rowKeyFromInstanceKey('wdtt-server:default')).toEqual({
			key: 'wdtt:server:default',
			role: 'server',
		});
		expect(rowKeyFromInstanceKey('freeturn-server:s2')).toEqual({
			key: 'freeturn:server:s2',
			role: 'server',
		});
	});

	it('ключ строки совпадает с тем, что собирает exitRows', () => {
		const rows = exitRows(sources([instance('nl', true)], [client('nl', true)]));
		expect(rowKeyFromInstanceKey('wdtt-client:nl')?.key).toBe(rows[0].key);
	});

	it('неизвестный вид, пустой id и мусор — null', () => {
		expect(rowKeyFromInstanceKey('singbox-client:nl')).toBeNull();
		expect(rowKeyFromInstanceKey('wdtt-client:')).toBeNull();
		expect(rowKeyFromInstanceKey('wdtt-client')).toBeNull();
		expect(rowKeyFromInstanceKey('')).toBeNull();
	});

	it('двоеточие в id ключ не рвёт', () => {
		expect(rowKeyFromInstanceKey('wdtt-client:a:b')?.key).toBe('wdtt:client:a:b');
	});
});
