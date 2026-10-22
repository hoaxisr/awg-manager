import '@testing-library/jest-dom/vitest';
import { expect, it, vi } from 'vitest';
import { fireEvent, render } from '@testing-library/svelte';
import type { ZapretStatus } from '$lib/types';
import ZapretStatusToggle from './ZapretStatusToggle.svelte';

function status(running: boolean, conflict = false): ZapretStatus {
	return {
		installed: true,
		conflict,
		running,
		processState: running ? 'running' : 'stopped',
		initScript: '/opt/etc/init.d/S90zapret',
		strategyCount: 0,
		hostlistCount: 0,
		hasUserHostlist: false
	};
}

it.each([
	{ running: false, action: 'start' as const },
	{ running: true, action: 'stop' as const }
])('reflects the running state and requests $action', async ({ running, action }) => {
	const oncontrol = vi.fn();
	const { getByRole } = render(ZapretStatusToggle, { props: { status: status(running), oncontrol } });
	const toggle = getByRole('checkbox');

	expect(toggle).toHaveProperty('checked', running);
	expect(toggle).toHaveAccessibleName();
	expect(toggle.parentElement).toHaveClass('flip');
	await fireEvent.input(toggle, { target: { checked: !(toggle as HTMLInputElement).checked } });
	expect(oncontrol).toHaveBeenCalledWith(action);
	expect(toggle).toHaveProperty('checked', running);
});

it('disables the toggle while busy', async () => {
	const oncontrol = vi.fn();
	const { getByRole } = render(ZapretStatusToggle, {
		props: { status: status(false), oncontrol, busy: true }
	});
	const toggle = getByRole('checkbox');

	expect(toggle).toBeDisabled();
	expect(toggle.closest('[aria-busy]')).toHaveAttribute('aria-busy', 'true');
	await fireEvent.input(toggle, { target: { checked: !(toggle as HTMLInputElement).checked } });
	expect(oncontrol).not.toHaveBeenCalled();
});

it('disables mutations when installations conflict', async () => {
	const oncontrol = vi.fn();
	const { getByRole } = render(ZapretStatusToggle, {
		props: { status: status(true, true), oncontrol }
	});
	const toggle = getByRole('checkbox');

	expect(toggle).toBeDisabled();
	expect(toggle).toBeChecked();
	await fireEvent.input(toggle, { target: { checked: !(toggle as HTMLInputElement).checked } });
	expect(oncontrol).not.toHaveBeenCalled();
});
