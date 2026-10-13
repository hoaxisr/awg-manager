import { describe, it, expect, afterEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import RebindCard from './RebindCard.svelte';
import LogsLiveIndicator from './LogsLiveIndicator.svelte';
import StaticRecordsCard from './StaticRecordsCard.svelte';
import { locale } from '$lib/i18n';
import { tunnelNameError } from '$lib/utils/tunnelName';
import { awgPingStatusNote } from '$lib/utils/awgPingStatus';
import { describeRouterReference } from '$lib/utils/tunnelRefs';
import { editTunnelSchema } from '$lib/schemas/tunnel';
import { awgParamHints } from '$lib/utils/awgParamHints';
import { getPlannedTests } from './ChecksGroup.svelte';
import type { TunnelListItem } from '$lib/types';

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

describe('diagnostics i18n', () => {
	it('RebindCard: заголовок, статус и подписи переключаются на английский', () => {
		render(RebindCard, { props: { rebind: { enabled: true, nets: ['10.0.0.0/8'], excludes: [] } } });
		expect(screen.getByText('Rebind-защита')).toBeTruthy();
		expect(screen.getByText('включена')).toBeTruthy();
		expect(screen.getByText('Защищённые сети')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('Rebind protection')).toBeTruthy();
		expect(screen.getByText('enabled')).toBeTruthy();
		expect(screen.getByText('Protected networks')).toBeTruthy();
	});

	it('LogsLiveIndicator: множественные формы счётчика записей', () => {
		const { container } = render(LogsLiveIndicator, { props: { paused: false, bufferCount: 0, entries: 1 } });
		const count = () => container.querySelector('.entries-count')?.textContent;
		expect(count()).toBe('1 запись');

		locale.set('en');
		flushSync();
		expect(count()).toBe('1 entry');
	});

	it('LogsLiveIndicator: русские формы one/few/many', () => {
		const forms = [1, 2, 5, 21].map((entries) => {
			const { container, unmount } = render(LogsLiveIndicator, { props: { paused: false, bufferCount: 0, entries } });
			const text = container.querySelector('.entries-count')?.textContent;
			unmount();
			return text;
		});
		expect(forms).toEqual(['1 запись', '2 записи', '5 записей', '21 запись']);
	});

	it('StaticRecordsCard: заголовок карточки', () => {
		render(StaticRecordsCard, { props: { records: [{ host: 'a.test', type: 'A', value: '1.1.1.1', flag: 0 }] } });
		expect(screen.getByText('Статические записи')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('Static records')).toBeTruthy();
	});

	it('tunnelNameError: сообщение с параметром на двух языках', () => {
		const long = 'ж'.repeat(128) + 'a';
		expect(tunnelNameError(long)).toBe('имя туннеля длиннее 256 байт (ограничение роутера)');
		locale.set('en');
		expect(tunnelNameError(long)).toBe('tunnel name is longer than 256 bytes (router limit)');
	});

	it('awgPingStatusNote: подпись и параметр счётчика', () => {
		const tunnel = {
			status: 'running',
			pingCheck: { status: 'recovering', restartCount: 3 },
			statusDetails: '',
		} as unknown as TunnelListItem;
		expect(awgPingStatusNote(tunnel, 'full')?.text).toBe('Восстановление (3)');
		expect(awgPingStatusNote(tunnel)?.text).toBe('Восст. (3)');
		locale.set('en');
		expect(awgPingStatusNote(tunnel, 'full')?.text).toBe('Recovering (3)');
	});

	it('describeRouterReference и подсказки AWG берут текст при вызове', () => {
		const loc = 'route.rules[4]';
		expect(describeRouterReference(loc).text).toBe('Используется в правиле #4');
		expect(awgParamHints().jc).toContain('Диапазон: 0-128');
		locale.set('en');
		expect(describeRouterReference(loc).text).toBe('Used in rule #4');
		expect(awgParamHints().jc).toContain('Range: 0-128');
	});

	it('getPlannedTests: подписи проверок переключаются вместе с языком', () => {
		expect(getPlannedTests(true, false)[0]).toBe('WAN связность');
		locale.set('en');
		expect(getPlannedTests(true, false)[0]).toBe('WAN connectivity');
	});

	it('editTunnelSchema: сообщение валидации формируется в момент проверки', () => {
		const messageFor = () => {
			const res = editTunnelSchema.safeParse({ name: '', address: 'x', endpoint: 'h:1', allowedIPs: '0.0.0.0/0' });
			return res.success ? '' : res.error.issues.find((i) => i.path[0] === 'name')?.message;
		};
		expect(messageFor()).toBe('Название обязательно');
		locale.set('en');
		expect(messageFor()).toBe('Name is required');
	});
});
