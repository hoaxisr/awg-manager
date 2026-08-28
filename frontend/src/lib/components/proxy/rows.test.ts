import { describe, it, expect } from 'vitest';
import type {
	WdttClientConfig,
	WdttClientInstance,
	WdttConfig,
	WdttInstanceStatus,
	WdttProcessStatus,
} from '$lib/types';
import { exitRows } from './rows';

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
