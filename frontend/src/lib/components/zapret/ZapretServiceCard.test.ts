import '@testing-library/jest-dom/vitest';
import { expect, it, vi } from 'vitest';
import { fireEvent, render } from '@testing-library/svelte';
import type { ZapretStatus } from '$lib/types';
import ZapretServiceCard from './ZapretServiceCard.svelte';

const status: ZapretStatus = {
	installed: true,
	flavor: 'zapret2',
	base: '/opt/zapret2',
	running: false,
	processState: 'stopped',
	initScript: '/opt/etc/init.d/S90zapret',
	strategyCount: 12,
	hostlistCount: 4,
	hasUserHostlist: true,
	pids: []
};

it('keeps settings navigation icon-only and toggles service state', async () => {
	const oncontrol = vi.fn();
	const onopen = vi.fn();
	const { getByRole } = render(ZapretServiceCard, {
		props: { status, oncontrol, onopen }
	});
	const settings = getByRole('button');
	const toggle = getByRole('checkbox');

	expect(toggle).toHaveAccessibleName();
	expect(settings).toHaveAccessibleName();
	expect(settings.textContent?.trim()).toBe('');
	await fireEvent.click(settings);
	expect(onopen).toHaveBeenCalledOnce();
	await fireEvent.input(toggle, { target: { checked: true } });
	expect(oncontrol).toHaveBeenCalledWith('start');
});
