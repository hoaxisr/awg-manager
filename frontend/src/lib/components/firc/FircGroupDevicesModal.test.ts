import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import { notifications } from '$lib/stores/notifications';
import type { FircDeviceSelector, FircHost, FircPolicy } from '$lib/types/firc';
import FircGroupDevicesModal from './FircGroupDevicesModal.svelte';

vi.mock('$lib/stores/notifications', () => ({
	notifications: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

const samplePolicies: FircPolicy[] = [
	{ name: 'Policy1', description: 'Main Policy', devices: 2 },
	{ name: 'ProxyRu', description: 'Proxy Ru', devices: 1 },
];

const sampleHosts: FircHost[] = [
	{
		name: 'Laptop',
		mac: 'AA:BB:CC:DD:EE:01',
		ip: '192.168.50.10',
		ip6: [],
		online: true,
	},
	{
		name: 'Phone',
		mac: 'AA:BB:CC:DD:EE:02',
		ip: '192.168.50.20',
		ip6: [],
		online: true,
	},
	{
		name: 'SmartTV',
		mac: 'AA:BB:CC:DD:EE:03',
		ip: '192.168.50.30',
		ip6: [],
		online: false,
	},
];

function renderModal(overrides?: {
	initialDevices?: FircDeviceSelector;
	hosts?: FircHost[];
	policies?: FircPolicy[];
}) {
	const onapply = vi.fn<(selector: FircDeviceSelector) => void>();
	const onclose = vi.fn<() => void>();
	const result = render(FircGroupDevicesModal, {
		props: {
			open: true,
			groupName: 'YouTube',
			initialDevices: overrides?.initialDevices,
			hosts: overrides?.hosts ?? sampleHosts,
			policies: overrides?.policies ?? samplePolicies,
			onapply,
			onclose,
		},
	});
	return { ...result, onapply, onclose };
}

describe('FircGroupDevicesModal', () => {
	beforeEach(() => {
		vi.clearAllMocks();
	});

	afterEach(() => {
		cleanup();
	});

	it('turns all toggles ON by default in all-devices mode and records turned-off toggles as deny', async () => {
		const { onapply } = renderModal();

		expect(screen.getByTestId('firc-dm-policies-count')).toHaveTextContent('2 / 2');
		expect(screen.getByTestId('firc-dm-hosts-count')).toHaveTextContent('3 / 3');
		expect(screen.getByTestId('firc-dm-summary')).toHaveTextContent(
			m.firc_devices_summary_all_active(),
		);

		const laptopToggle = screen.getByRole('checkbox', { name: 'Laptop' });
		const policy1Toggle = screen.getByRole('checkbox', { name: 'Main Policy' });
		expect(laptopToggle).toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Phone' })).toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'SmartTV' })).toBeChecked();
		expect(policy1Toggle).toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Proxy Ru' })).toBeChecked();

		await fireEvent.click(laptopToggle);
		await fireEvent.click(policy1Toggle);

		expect(laptopToggle).not.toBeChecked();
		expect(policy1Toggle).not.toBeChecked();
		expect(screen.getByTestId('firc-dm-policies-count')).toHaveTextContent('1 / 2');
		expect(screen.getByTestId('firc-dm-hosts-count')).toHaveTextContent('2 / 3');
		expect(screen.getByTestId('firc-dm-summary')).toHaveTextContent(
			m.firc_devices_summary_excluded({ count: 2 }),
		);

		const laptopRow = document.body.querySelector('[data-entry="mac:aa:bb:cc:dd:ee:01"]');
		const policyRow = document.body.querySelector('[data-entry="policy:Policy1"]');
		expect(laptopRow).toHaveAttribute('data-enabled', 'false');
		expect(policyRow).toHaveAttribute('data-enabled', 'false');
		expect(screen.getAllByText(m.firc_devices_badge_excluded())).toHaveLength(2);

		await fireEvent.click(screen.getByRole('button', { name: m.common_apply() }));

		expect(onapply).toHaveBeenCalledTimes(1);
		const applied = onapply.mock.calls[0][0];
		expect(applied.allow).toEqual([]);
		expect(applied.deny).toHaveLength(2);
		expect(applied.deny).toEqual(expect.arrayContaining(['policy:Policy1', 'mac:aa:bb:cc:dd:ee:01']));
	});

	it('opens pre-existing exclusions as OFF toggles and restores all via enable-all button', async () => {
		const { onapply } = renderModal({
			initialDevices: {
				allow: [],
				deny: ['policy:Policy1', 'mac:aa:bb:cc:dd:ee:01'],
			},
		});

		expect(screen.getByRole('checkbox', { name: 'Main Policy' })).not.toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Laptop' })).not.toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Proxy Ru' })).toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Phone' })).toBeChecked();
		expect(screen.getByTestId('firc-dm-policies-count')).toHaveTextContent('1 / 2');
		expect(screen.getByTestId('firc-dm-hosts-count')).toHaveTextContent('2 / 3');

		await fireEvent.click(screen.getByRole('button', { name: m.firc_devices_enable_all() }));

		expect(screen.getByRole('checkbox', { name: 'Main Policy' })).toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Laptop' })).toBeChecked();
		expect(screen.getByTestId('firc-dm-policies-count')).toHaveTextContent('2 / 2');
		expect(screen.getByTestId('firc-dm-hosts-count')).toHaveTextContent('3 / 3');

		await fireEvent.click(screen.getByRole('button', { name: m.common_apply() }));
		expect(onapply).toHaveBeenCalledWith({ allow: [], deny: [] });
	});

	it('starts all toggles OFF in selected-only mode, warns if empty, and saves selected items as allow', async () => {
		const { onapply } = renderModal();

		await fireEvent.click(screen.getByRole('button', { name: m.firc_devices_mode_allow() }));

		expect(screen.getByTestId('firc-dm-policies-count')).toHaveTextContent('0 / 2');
		expect(screen.getByTestId('firc-dm-hosts-count')).toHaveTextContent('0 / 3');
		expect(screen.getByRole('checkbox', { name: 'Laptop' })).not.toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Main Policy' })).not.toBeChecked();

		await fireEvent.click(screen.getByRole('button', { name: m.common_apply() }));
		expect(notifications.warning).toHaveBeenCalledWith(m.firc_devices_warn_select_one());
		expect(onapply).not.toHaveBeenCalled();

		await fireEvent.click(screen.getByRole('checkbox', { name: 'Phone' }));
		await fireEvent.click(screen.getByRole('checkbox', { name: 'Proxy Ru' }));

		expect(screen.getByTestId('firc-dm-policies-count')).toHaveTextContent('1 / 2');
		expect(screen.getByTestId('firc-dm-hosts-count')).toHaveTextContent('1 / 3');
		expect(screen.getByTestId('firc-dm-summary')).toHaveTextContent(
			m.firc_devices_summary_selected({ count: 2 }),
		);

		await fireEvent.click(screen.getByRole('button', { name: m.common_apply() }));
		expect(onapply).toHaveBeenCalledTimes(1);
		const applied = onapply.mock.calls[0][0];
		expect(applied.deny).toEqual([]);
		expect(applied.allow).toHaveLength(2);
		expect(applied.allow).toEqual(expect.arrayContaining(['policy:ProxyRu', 'mac:aa:bb:cc:dd:ee:02']));
	});

	it('preserves independent draft selections when switching between modes', async () => {
		const { onapply } = renderModal();

		await fireEvent.click(screen.getByRole('checkbox', { name: 'Laptop' }));
		expect(screen.getByRole('checkbox', { name: 'Laptop' })).not.toBeChecked();

		await fireEvent.click(screen.getByRole('button', { name: m.firc_devices_mode_allow() }));
		await fireEvent.click(screen.getByRole('checkbox', { name: 'Phone' }));
		expect(screen.getByRole('checkbox', { name: 'Phone' })).toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Laptop' })).not.toBeChecked();

		await fireEvent.click(screen.getByRole('button', { name: m.firc_devices_mode_all() }));
		expect(screen.getByRole('checkbox', { name: 'Laptop' })).not.toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Phone' })).toBeChecked();

		await fireEvent.click(screen.getByRole('button', { name: m.common_apply() }));
		expect(onapply).toHaveBeenCalledWith({
			allow: [],
			deny: ['mac:aa:bb:cc:dd:ee:01'],
		});
	});

	it('adds and removes manual IP/CIDR/MAC entries in both modes', async () => {
		const { onapply } = renderModal();

		const manualInput = screen.getByPlaceholderText(m.firc_devices_manual_placeholder_exclude());
		await fireEvent.input(manualInput, { target: { value: '192.168.50.99' } });
		await fireEvent.click(screen.getByRole('button', { name: m.firc_devices_manual_btn_exclude() }));

		expect(screen.getByText('192.168.50.99')).toBeInTheDocument();
		expect(screen.getByTestId('firc-dm-summary')).toHaveTextContent(
			m.firc_devices_summary_excluded({ count: 1 }),
		);

		await fireEvent.click(screen.getByRole('button', { name: m.firc_devices_chip_remove() }));
		expect(screen.queryByText('192.168.50.99')).not.toBeInTheDocument();
		expect(screen.getByTestId('firc-dm-summary')).toHaveTextContent(
			m.firc_devices_summary_all_active(),
		);

		await fireEvent.click(screen.getByRole('button', { name: m.firc_devices_mode_allow() }));
		const includeInput = screen.getByPlaceholderText(m.firc_devices_manual_placeholder_include());
		await fireEvent.input(includeInput, { target: { value: '10.0.0.0/24' } });
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));

		expect(screen.getByText('10.0.0.0/24')).toBeInTheDocument();
		await fireEvent.click(screen.getByRole('button', { name: m.common_apply() }));
		expect(onapply).toHaveBeenCalledWith({ allow: ['10.0.0.0/24'], deny: [] });
	});

	it('filters host list by search query while preserving toggle states', async () => {
		renderModal();

		await fireEvent.click(screen.getByRole('checkbox', { name: 'Laptop' }));

		const searchInput = screen.getByPlaceholderText(m.firc_devices_search_placeholder());
		await fireEvent.input(searchInput, { target: { value: '192.168.50.10' } });

		expect(screen.getByRole('checkbox', { name: 'Laptop' })).not.toBeChecked();
		expect(screen.queryByRole('checkbox', { name: 'Phone' })).not.toBeInTheDocument();
		expect(screen.queryByRole('checkbox', { name: 'SmartTV' })).not.toBeInTheDocument();

		await fireEvent.input(searchInput, { target: { value: 'nonexistent-device' } });
		expect(
			screen.getByText(m.firc_devices_search_empty({ query: 'nonexistent-device' })),
		).toBeInTheDocument();

		await fireEvent.click(screen.getByRole('button', { name: m.common_clear() }));
		expect(screen.getByRole('checkbox', { name: 'Laptop' })).not.toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Phone' })).toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'SmartTV' })).toBeChecked();
	});

	it('excludes and locks member device toggles and updates active hosts counter when a policy is excluded', async () => {
		const hostsWithPolicies: FircHost[] = [
			{
				name: 'Laptop',
				mac: 'AA:BB:CC:DD:EE:01',
				ip: '192.168.50.10',
				ip6: [],
				active: true,
				policy: 'Policy1',
			},
			{
				name: 'Tablet',
				mac: 'AA:BB:CC:DD:EE:04',
				ip: '192.168.50.11',
				ip6: [],
				online: true,
				policy: 'Policy1',
			},
			{
				name: 'Phone',
				mac: 'AA:BB:CC:DD:EE:02',
				ip: '192.168.50.20',
				ip6: [],
				online: true,
				policy: 'ProxyRu',
			},
			{
				name: 'SmartTV',
				mac: 'AA:BB:CC:DD:EE:03',
				ip: '192.168.50.30',
				ip6: [],
				online: false,
			},
		];
		const { onapply } = renderModal({ hosts: hostsWithPolicies });

		expect(screen.getByTestId('firc-dm-policies-count')).toHaveTextContent('2 / 2');
		expect(screen.getByTestId('firc-dm-hosts-count')).toHaveTextContent('4 / 4');

		const policy1Toggle = screen.getByRole('checkbox', { name: 'Main Policy' });
		await fireEvent.click(policy1Toggle);

		expect(screen.getByTestId('firc-dm-policies-count')).toHaveTextContent('1 / 2');
		expect(screen.getByTestId('firc-dm-hosts-count')).toHaveTextContent('2 / 4');

		const laptopToggle = screen.getByRole('checkbox', { name: 'Laptop' });
		const tabletToggle = screen.getByRole('checkbox', { name: 'Tablet' });
		const phoneToggle = screen.getByRole('checkbox', { name: 'Phone' });
		expect(laptopToggle).not.toBeChecked();
		expect(laptopToggle).toBeDisabled();
		expect(tabletToggle).not.toBeChecked();
		expect(tabletToggle).toBeDisabled();
		expect(phoneToggle).toBeChecked();
		expect(phoneToggle).not.toBeDisabled();
		expect(screen.getAllByText(m.firc_devices_badge_by_policy())).toHaveLength(2);

		await fireEvent.click(screen.getByRole('button', { name: m.common_apply() }));
		expect(onapply).toHaveBeenCalledWith({
			allow: [],
			deny: ['policy:Policy1'],
		});

		await fireEvent.click(policy1Toggle);
		expect(laptopToggle).toBeChecked();
		expect(laptopToggle).not.toBeDisabled();
		expect(tabletToggle).toBeChecked();
		expect(tabletToggle).not.toBeDisabled();
		expect(screen.getByTestId('firc-dm-hosts-count')).toHaveTextContent('4 / 4');
	});

	it('includes and locks member device toggles in selected-only mode and supports searching by policy name', async () => {
		const hostsWithPolicies: FircHost[] = [
			{
				name: 'Laptop',
				mac: 'AA:BB:CC:DD:EE:01',
				ip: '192.168.50.10',
				ip6: [],
				online: true,
				policy: 'Policy1',
			},
			{
				name: 'Tablet',
				mac: 'AA:BB:CC:DD:EE:04',
				ip: '192.168.50.11',
				ip6: [],
				online: true,
				policy: 'Policy1',
			},
			{
				name: 'Phone',
				mac: 'AA:BB:CC:DD:EE:02',
				ip: '192.168.50.20',
				ip6: [],
				online: true,
				policy: 'ProxyRu',
			},
		];
		const { onapply } = renderModal({ hosts: hostsWithPolicies });

		await fireEvent.click(screen.getByRole('button', { name: m.firc_devices_mode_allow() }));
		expect(screen.getByTestId('firc-dm-policies-count')).toHaveTextContent('0 / 2');
		expect(screen.getByTestId('firc-dm-hosts-count')).toHaveTextContent('0 / 3');

		await fireEvent.click(screen.getByRole('checkbox', { name: 'Main Policy' }));
		expect(screen.getByTestId('firc-dm-policies-count')).toHaveTextContent('1 / 2');
		expect(screen.getByTestId('firc-dm-hosts-count')).toHaveTextContent('2 / 3');

		const laptopToggle = screen.getByRole('checkbox', { name: 'Laptop' });
		const tabletToggle = screen.getByRole('checkbox', { name: 'Tablet' });
		const phoneToggle = screen.getByRole('checkbox', { name: 'Phone' });
		expect(laptopToggle).toBeChecked();
		expect(laptopToggle).toBeDisabled();
		expect(tabletToggle).toBeChecked();
		expect(tabletToggle).toBeDisabled();
		expect(phoneToggle).not.toBeChecked();
		expect(phoneToggle).not.toBeDisabled();
		expect(screen.getAllByText(m.firc_devices_badge_by_policy())).toHaveLength(2);

		const searchInput = screen.getByPlaceholderText(m.firc_devices_search_placeholder());
		await fireEvent.input(searchInput, { target: { value: 'Main Policy' } });
		expect(screen.getByRole('checkbox', { name: 'Laptop' })).toBeInTheDocument();
		expect(screen.getByRole('checkbox', { name: 'Tablet' })).toBeInTheDocument();
		expect(screen.queryByRole('checkbox', { name: 'Phone' })).not.toBeInTheDocument();

		await fireEvent.click(screen.getByRole('button', { name: m.common_apply() }));
		expect(onapply).toHaveBeenCalledWith({
			allow: ['policy:Policy1'],
			deny: [],
		});
	});

	it('recognizes and toggles legacy policy:<description> selectors cleanly', async () => {
		const hostsWithPolicies: FircHost[] = [
			{
				name: 'Laptop',
				mac: 'AA:BB:CC:DD:EE:01',
				ip: '192.168.50.10',
				ip6: [],
				online: true,
				policy: 'Policy1',
			},
			{
				name: 'Phone',
				mac: 'AA:BB:CC:DD:EE:02',
				ip: '192.168.50.20',
				ip6: [],
				online: true,
				policy: 'ProxyRu',
			},
		];
		const { onapply } = renderModal({
			hosts: hostsWithPolicies,
			initialDevices: { allow: [], deny: ['policy:Main Policy'] },
		});

		const policy1Toggle = screen.getByRole('checkbox', { name: 'Main Policy' });
		expect(policy1Toggle).not.toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Laptop' })).toBeDisabled();
		expect(screen.getByTestId('firc-dm-policies-count')).toHaveTextContent('1 / 2');

		// Turn Main Policy back ON -> removes legacy 'policy:Main Policy' entry cleanly.
		await fireEvent.click(policy1Toggle);
		expect(policy1Toggle).toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Laptop' })).not.toBeDisabled();
		await fireEvent.click(screen.getByRole('button', { name: m.common_apply() }));
		expect(onapply).toHaveBeenLastCalledWith({ allow: [], deny: [] });

		// Turn OFF again -> writes canonical 'policy:Policy1'.
		await fireEvent.click(policy1Toggle);
		await fireEvent.click(screen.getByRole('button', { name: m.common_apply() }));
		expect(onapply).toHaveBeenLastCalledWith({ allow: [], deny: ['policy:Policy1'] });
	});
});
