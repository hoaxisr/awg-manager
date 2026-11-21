import { describe, expect, it } from 'vitest';
import { m } from '$lib/i18n';
import type { FircStatus, FircTunnel, FircTunnelState } from '$lib/types/firc';
import {
	formatFircSkipReason,
	formatFircSubError,
	getFircTunnelDiagnostic,
} from './fircDiagnostics';

function baseTunnel(overrides: Partial<FircTunnel> = {}): FircTunnel {
	return {
		id: 'tunnel1',
		device: 'tunvless0',
		description: '',
		enable: true,
		active: 1,
		by: 'connection',
		interval: 60,
		silence: 20,
		filter: '',
		order: [],
		exclude: [],
		sources: [
			{
				id: 'sub1',
				kind: 'subscription',
				name: '999',
				url: 'https://example.com/sub',
				interval: 21600,
			},
		],
		uplink: { kind: 'auto', ref: '' },
		advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
		inbounds: [],
		...overrides,
	};
}

function baseState(overrides: Partial<FircTunnelState> = {}): FircTunnelState {
	return {
		id: 'tunnel1',
		device: 'tunvless0',
		status: 'waiting',
		since: 0,
		backoffS: 0,
		lastExit: 0,
		uplinkOk: true,
		tcpPort: 0,
		tcpSkipped: [],
		tcpOff: '',
		active: [],
		groups: [],
		nodes: [],
		subscriptions: [],
		rxBps: 0,
		txBps: 0,
		inbounds: [],
		...overrides,
	};
}

function baseStatus(overrides: Partial<FircStatus> = {}): FircStatus {
	return {
		fircdInstalled: true,
		tunvlessInstalled: true,
		running: true,
		selected: true,
		socketPath: '/tmp/firc/firc.sock',
		dnsProxyEndpoint: '127.0.0.1:3553',
		inboundPort: 1099,
		tunnelsCount: 1,
		groupsCount: 0,
		...overrides,
	};
}

describe('formatFircSkipReason', () => {
	it('translates known parser skip reasons and passes unknown ones through', () => {
		expect(formatFircSkipReason('allowInsecure: needs --insecure')).toBe(
			m.firc_skip_allow_insecure(),
		);
		expect(formatFircSkipReason('0.0.0.0: nobody to answer')).toBe(
			m.firc_skip_nowhere({ host: '0.0.0.0' }),
		);
		expect(formatFircSkipReason('over the 256-node limit')).toBe(m.firc_skip_over_cap());
		expect(formatFircSkipReason('reality without pbk')).toBe(m.firc_skip_no_pbk());
		expect(formatFircSkipReason('transport kcp is not supported')).toBe(
			m.firc_skip_unsupported_transport({ detail: 'kcp' }),
		);
		expect(formatFircSkipReason('custom unmapped reason')).toBe('custom unmapped reason');
	});
});

describe('formatFircSubError', () => {
	it('translates known subscription fetch errors and passes unknown ones through', () => {
		expect(formatFircSubError('the server answered: Device limit reached')).toBe(
			m.firc_sub_err_placeholder({ said: 'Device limit reached' }),
		);
		expect(formatFircSubError('no nodes in the answer')).toBe(m.firc_sub_err_no_nodes());
		expect(formatFircSubError('HTTP 403')).toBe(m.firc_sub_err_http({ code: '403' }));
		expect(formatFircSubError('no answer')).toBe(m.firc_sub_err_no_answer());
		expect(formatFircSubError('the uplink mark could not be set')).toBe(m.firc_sub_err_mark());
		expect(formatFircSubError('over 4194304 bytes or too many redirects')).toBe(
			m.firc_sub_err_limit(),
		);
		expect(formatFircSubError('unexpected error text')).toBe('unexpected error text');
	});
});

describe('getFircTunnelDiagnostic', () => {
	it('handles disabled and running states without warnings', () => {
		expect(
			getFircTunnelDiagnostic({
				tunnel: baseTunnel({ enable: false }),
				status: baseStatus(),
			}),
		).toEqual({
			stateLabel: m.firc_state_off(),
			tone: 'muted',
			reason: '',
		});

		expect(
			getFircTunnelDiagnostic({
				tunnel: baseTunnel(),
				live: baseState({ status: 'up', active: ['India'] }),
				status: baseStatus(),
			}),
		).toEqual({
			stateLabel: m.firc_state_running(),
			tone: 'ok',
			reason: '',
		});
	});

	it('explains why live telemetry is missing (not selected, daemon stopped, recovery)', () => {
		const unselected = getFircTunnelDiagnostic({
			tunnel: baseTunnel(),
			status: baseStatus({ selected: false, running: false }),
		});
		expect(unselected.stateLabel).toBe(m.firc_state_not_selected());
		expect(unselected.tone).toBe('warning');
		expect(unselected.reason).toBe(m.firc_reason_engine_not_selected());

		const stopped = getFircTunnelDiagnostic({
			tunnel: baseTunnel(),
			status: baseStatus({ selected: true, running: false }),
		});
		expect(stopped.stateLabel).toBe(m.firc_state_daemon_stopped());
		expect(stopped.tone).toBe('error');
		expect(stopped.reason).toBe(m.firc_reason_daemon_stopped());

		const recovery = getFircTunnelDiagnostic({
			tunnel: baseTunnel(),
			status: baseStatus({
				selected: true,
				running: false,
				recoveryRequired: true,
				recoveryReason: 'broken config',
			}),
		});
		expect(recovery.stateLabel).toBe(m.firc_state_recovery());
		expect(recovery.tone).toBe('error');
		expect(recovery.reason).toBe(m.firc_reason_recovery_detail({ reason: 'broken config' }));
	});

	it('prioritizes subscription errors and fetching state when waiting', () => {
		const singleErr = getFircTunnelDiagnostic({
			tunnel: baseTunnel(),
			live: baseState({
				status: 'waiting',
				subscriptions: [
					{
						name: '999',
						nodes: 0,
						fetching: false,
						lastOk: 0,
						lastTry: 100,
						error: 'HTTP 403',
					},
				],
			}),
			status: baseStatus(),
		});
		expect(singleErr.stateLabel).toBe(m.firc_state_sub_error());
		expect(singleErr.tone).toBe('error');
		expect(singleErr.reason).toBe(m.firc_sub_err_http({ code: '403' }));

		const multiErr = getFircTunnelDiagnostic({
			tunnel: baseTunnel(),
			live: baseState({
				status: 'waiting',
				subscriptions: [
					{
						name: 'Backup',
						nodes: 5,
						fetching: false,
						lastOk: 100,
						lastTry: 100,
						error: '',
					},
					{
						name: 'Main',
						nodes: 0,
						fetching: false,
						lastOk: 0,
						lastTry: 100,
						error: 'no answer',
					},
				],
			}),
			status: baseStatus(),
		});
		expect(multiErr.reason).toBe(`Main: ${m.firc_sub_err_no_answer()}`);

		const fetching = getFircTunnelDiagnostic({
			tunnel: baseTunnel(),
			live: baseState({
				status: 'waiting',
				subscriptions: [
					{
						name: '999',
						nodes: 0,
						fetching: true,
						lastOk: 0,
						lastTry: 100,
						error: '',
					},
				],
			}),
			status: baseStatus(),
		});
		expect(fetching).toEqual({
			stateLabel: m.firc_state_fetching(),
			tone: 'info',
			reason: m.firc_reason_sub_fetching(),
		});
	});

	it('diagnoses all skipped nodes, all excluded nodes, empty sources, and non-matching filter', () => {
		const allSkipped = getFircTunnelDiagnostic({
			tunnel: baseTunnel(),
			live: baseState({
				status: 'waiting',
				nodes: [
					{
						key: 'sub1:a',
						name: 'A',
						source: '999',
						state: 'skipped',
						since: 0,
						skipReason: 'allowInsecure: needs --insecure',
					},
					{
						key: 'sub1:b',
						name: 'B',
						source: '999',
						state: 'skipped',
						since: 0,
						skipReason: 'allowInsecure: needs --insecure',
					},
				],
			}),
			status: baseStatus(),
		});
		expect(allSkipped.stateLabel).toBe(m.firc_state_nodes_skipped());
		expect(allSkipped.tone).toBe('error');
		expect(allSkipped.reason).toBe(
			m.firc_reason_all_nodes_skipped({
				count: 2,
				reason: m.firc_skip_allow_insecure(),
			}),
		);

		const allExcluded = getFircTunnelDiagnostic({
			tunnel: baseTunnel(),
			live: baseState({
				status: 'waiting',
				nodes: [{ key: 'sub1:a', name: 'A', source: '999', state: 'excluded', since: 0 }],
			}),
			status: baseStatus(),
		});
		expect(allExcluded.stateLabel).toBe(m.firc_state_all_excluded());
		expect(allExcluded.tone).toBe('warning');
		expect(allExcluded.reason).toBe(m.firc_reason_all_nodes_excluded());

		const noSources = getFircTunnelDiagnostic({
			tunnel: baseTunnel({ sources: [] }),
			live: baseState({ status: 'waiting' }),
			status: baseStatus(),
		});
		expect(noSources.stateLabel).toBe(m.firc_state_no_sources());
		expect(noSources.reason).toBe(m.firc_reason_no_sources());

		const filterEmpty = getFircTunnelDiagnostic({
			tunnel: baseTunnel({ filter: 'Finland' }),
			live: baseState({ status: 'waiting' }),
			status: baseStatus(),
		});
		expect(filterEmpty.stateLabel).toBe(m.firc_state_no_nodes());
		expect(filterEmpty.reason).toBe(m.firc_reason_filter_empty({ filter: 'Finland' }));
	});

	it('handles non-waiting runtime failure and startup statuses', () => {
		expect(
			getFircTunnelDiagnostic({
				tunnel: baseTunnel(),
				live: baseState({ status: 'no_node' }),
				status: baseStatus(),
			}),
		).toEqual({
			stateLabel: m.firc_state_no_node(),
			tone: 'error',
			reason: m.firc_reason_no_node(),
		});

		expect(
			getFircTunnelDiagnostic({
				tunnel: baseTunnel(),
				live: baseState({ status: 'uplink_down' }),
				status: baseStatus(),
			}),
		).toEqual({
			stateLabel: m.firc_state_uplink_down(),
			tone: 'error',
			reason: m.firc_reason_uplink_down(),
		});

		expect(
			getFircTunnelDiagnostic({
				tunnel: baseTunnel(),
				live: baseState({ status: 'bad_config' }),
				status: baseStatus(),
			}),
		).toEqual({
			stateLabel: m.firc_state_bad_config(),
			tone: 'error',
			reason: m.firc_reason_bad_config(),
		});

		expect(
			getFircTunnelDiagnostic({
				tunnel: baseTunnel(),
				live: baseState({ status: 'backoff', backoffS: 12 }),
				status: baseStatus(),
			}),
		).toEqual({
			stateLabel: m.firc_state_backoff(),
			tone: 'error',
			reason: m.firc_reason_backoff_sec({ seconds: 12 }),
		});

		expect(
			getFircTunnelDiagnostic({
				tunnel: baseTunnel(),
				live: baseState({ status: 'starting' }),
				status: baseStatus(),
			}),
		).toEqual({
			stateLabel: m.firc_state_starting(),
			tone: 'info',
			reason: m.firc_reason_starting(),
		});

		expect(
			getFircTunnelDiagnostic({
				tunnel: baseTunnel(),
				live: baseState({ status: 'router', why: 'waiting for the router' }),
				status: baseStatus(),
			}),
		).toEqual({
			stateLabel: m.firc_state_router(),
			tone: 'warning',
			reason: m.firc_why_waiting_router(),
		});
	});
});
