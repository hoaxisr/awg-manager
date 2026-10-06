import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import SusaninAdaptiveTab from './SusaninAdaptiveTab.svelte';

vi.mock('$lib/api/client', () => ({
	api: {
		getAdaptiveRoutingStatus: vi.fn().mockResolvedValue({
			settings: {
				enabled: false,
				routingTableId: 105,
				fwmarkMask: '0x30000000',
				fwmarkTest: '0x10000000',
				fwmarkOk: '0x20000000',
				rulePriorityTest: 96,
				rulePriorityOk: 95,
				source: { type: 'all_lan' },
				primaryEgress: { kind: 'mihomo-group', resourceId: 'grp-1', engine: 'mihomo' },
				failurePolicy: 'direct',
				detection: {
					fastIntervalSeconds: 1,
					softIntervalSeconds: 1,
					judgeIntervalSeconds: 1,
					healthIntervalSeconds: 5,
					tcpSynRetries: 2,
					lateStallBytes: 1500,
				},
				persistence: { okTtlSeconds: 0, maxEntries: 4096, separateTcpUdp: true },
				alwaysFileEnabled: true,
				neverFileEnabled: true,
				alwaysEntries: ['custom-always.com'],
				neverEntries: ['bank.ru'],
			},
			state: {
				appliedGeneration: 'gen-1',
				routingOwner: 'susanin',
				status: 'running',
				learnedTcpCount: 14,
				learnedUdpCount: 5,
				testingTcpCount: 2,
				testingUdpCount: 1,
				alwaysCount: 1,
				neverCount: 1,
				lastReconcile: '2026-09-22T20:00:00Z',
			},
		}),
		getAdaptiveRoutingEgresses: vi.fn().mockResolvedValue({
			items: [{
				ref: { kind: 'mihomo-group', resourceId: 'grp-1', engine: 'mihomo' },
				displayName: 'Быстрый прокси',
				interface: 'awgsus0',
				capabilities: { tcp: true, udp: true, icmp: false, ipv4: true, ipv6: false },
				available: true,
			}],
		}),
		getAdaptiveRoutingLearned: vi.fn().mockResolvedValue({
			always: ['custom-always.com'],
			never: ['bank.ru'],
			okTcpCount: 14,
			okUdpCount: 5,
			testTcpCount: 2,
			testUdpCount: 1,
		}),
		applyAdaptiveRouting: vi.fn().mockResolvedValue({
			settings: { enabled: true },
			state: { status: 'running', routingOwner: 'susanin' },
		}),
		startAdaptiveRouting: vi.fn().mockResolvedValue({ state: { status: 'running', routingOwner: 'susanin' } }),
		stopAdaptiveRouting: vi.fn().mockResolvedValue({ state: { status: 'stopped', routingOwner: 'none' } }),
		clearAdaptiveRoutingCache: vi.fn().mockResolvedValue({ cleared: true }),
		testAdaptiveRoutingEgress: vi.fn().mockResolvedValue({ available: true, interface: 'awgsus0' }),
		getAdaptiveRoutingLogs: vi.fn().mockResolvedValue({ events: [] }),
	},
}));

describe('SusaninAdaptiveTab', () => {
	it('renders status bar with running state and routing owner badge', async () => {
		render(SusaninAdaptiveTab);
		expect(await screen.findByText('Susanin активен')).toBeDefined();
		expect(screen.getByText('Владелец:')).toBeDefined();
		expect(screen.getByText('Susanin')).toBeDefined();
		expect(screen.getByText('Fail-Open (Direct)')).toBeDefined();
	});

	it('renders the routing and learning workspaces', async () => {
		render(SusaninAdaptiveTab);
		expect(await screen.findByText('Маршрутизация и выход')).toBeDefined();
		expect(screen.getByText('Обучение и списки')).toBeDefined();
		expect(screen.getByText('Выход для обхода блокировок')).toBeDefined();
		expect(screen.getByText('Источник трафика')).toBeDefined();
		expect(screen.getByText('Поведение при сбое выхода')).toBeDefined();
	});

	it('displays learning statistics returned by the backend', async () => {
		render(SusaninAdaptiveTab);
		expect(await screen.findByText('TCP ОК')).toBeDefined();
		expect(screen.getByText('UDP ОК')).toBeDefined();
		expect(screen.getByText('В тесте')).toBeDefined();
		expect((await screen.findAllByText('14')).length).toBeGreaterThan(0);
		expect((await screen.findAllByText('5')).length).toBeGreaterThan(0);
	});

	it('keeps low-level routing marks out of the user interface', async () => {
		const { container } = render(SusaninAdaptiveTab);
		await screen.findByText('Маршрутизация и выход');
		expect(container.textContent).not.toContain('Fwmark');
		expect(container.textContent).not.toContain('0x30000000');
		expect(screen.getByText('Всегда через VPN (Always)')).toBeDefined();
	});
});
