// Автозавод AWG-туннеля из детали «Выхода»: ручка зовётся сама, поэтому
// «конфиг ещё не приехал» обязано молчать, а всё остальное — говорить.
import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { AccessPolicy, TunnelListItem, WdttClientConfig, WdttProcessStatus } from '$lib/types';

// Баррель $lib/components/ui тянет theme-store: тот читает matchMedia на импорте.
vi.hoisted(() => {
	Object.defineProperty(globalThis, 'matchMedia', {
		writable: true,
		configurable: true,
		value: (query: string) => ({
			matches: false,
			media: query,
			onchange: null,
			addEventListener: () => {},
			removeEventListener: () => {},
			addListener: () => {},
			removeListener: () => {},
			dispatchEvent: () => false,
		}),
	});
});

const apiMock = vi.hoisted(() => ({
	ensureWdttWgTunnel: vi.fn(),
	ensureWdttRawTunnel: vi.fn(),
	importConfig: vi.fn(),
	getFreeTurnCaptchaStatus: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({ api: apiMock }));

const notify = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }));
vi.mock('$lib/stores/notifications', () => ({ notifications: notify }));

import { render, waitFor } from '@testing-library/svelte';
import ExitDetail from './ExitDetail.svelte';
import type { ProxyInstanceRow } from './rows';

const CLIENT: WdttClientConfig = {
	listen: '127.0.0.1:9000',
	peer: 'vps.example:56002',
	password: '',
	vkHashes: 'h1',
	workers: 9,
	obfs: 'audio',
	fingerprint: '',
	captchaMode: 'auto',
	connMode: 'wg',
};

function row(id: string, mode: 'wg' | 'raw'): ProxyInstanceRow {
	return {
		key: `wdtt:client:${id}`,
		id,
		protocol: 'wdtt',
		role: 'client',
		name: 'Нидерланды',
		state: 'running',
		autostart: true,
		orphanedPid: false,
		binaryPresent: true,
		mode,
	};
}

const status: WdttProcessStatus = {
	running: true,
	binary: '/opt/bin/wdtt-client',
	binaryPresent: true,
	wgConfig: '[Interface]\nPrivateKey = x\n',
};

function mount(id: string, mode: 'wg' | 'raw' = 'wg') {
	return render(ExitDetail, {
		props: {
			row: row(id, mode),
			status,
			wdttClient: { ...CLIENT, connMode: mode },
			policies: [] as AccessPolicy[],
			tunnels: [] as TunnelListItem[],
			onstart: () => {},
			onstop: () => {},
			onsave: async () => null,
			onreload: () => {},
		},
	});
}

function apiError(code: string, message: string): Error {
	const e: Error & { status?: number; body?: unknown } = new Error(message);
	e.status = code === 'WDTT_WG_NOT_READY' ? 409 : 400;
	e.body = { error: true, message, code };
	return e;
}

describe('ExitDetail: автозавод связанного туннеля', () => {
	beforeEach(() => {
		vi.clearAllMocks();
	});

	it('«конфиг ещё не получен» тревоги не поднимает: ручку дёргает автоэффект', async () => {
		apiMock.ensureWdttWgTunnel.mockRejectedValue(
			apiError('WDTT_WG_NOT_READY', 'WireGuard конфиг ещё не получен от wdtt-server'),
		);
		mount('silent-1');
		await waitFor(() => expect(apiMock.ensureWdttWgTunnel).toHaveBeenCalledWith('silent-1'));
		await new Promise((r) => setTimeout(r, 0));
		expect(notify.error).not.toHaveBeenCalled();
	});

	it('любой другой отказ виден пользователю', async () => {
		apiMock.ensureWdttWgTunnel.mockRejectedValue(
			apiError('WDTT_WG_IMPORT_FAILED', 'не удалось импортировать конфиг'),
		);
		mount('loud-1');
		await waitFor(() => expect(notify.error).toHaveBeenCalledWith('не удалось импортировать конфиг'));
	});

	it('в режиме Raw ручка не зовётся вовсе: зеркальную запись ведёт движок', async () => {
		mount('raw-1', 'raw');
		await new Promise((r) => setTimeout(r, 20));
		expect(apiMock.ensureWdttWgTunnel).not.toHaveBeenCalled();
		expect(apiMock.ensureWdttRawTunnel).not.toHaveBeenCalled();
	});
});
