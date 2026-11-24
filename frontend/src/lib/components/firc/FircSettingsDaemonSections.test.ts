import { afterEach, describe, expect, it } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, render } from '@testing-library/svelte';
import type { FircSettingValue } from '$lib/types/firc';
import FircSettingsDaemonSections from './FircSettingsDaemonSections.svelte';

function poolInputs(settings: Record<string, unknown>) {
	cleanup();
	const { container } = render(FircSettingsDaemonSections, {
		props: { settings: settings as Record<string, FircSettingValue>, onfield: () => {} },
	});
	const val = (id: string) => (container.querySelector(`#${id}`) as HTMLInputElement).value;
	return { ttl: val('firc-pool-ttl'), max: val('firc-pool-max'), v6: val('firc-pool-v6-chunk') };
}

describe('FircSettingsDaemonSections fallbacks', () => {
	afterEach(() => cleanup());

	it('shows the daemon defaults when the key is missing, null or empty', () => {
		const want = { ttl: '300', max: '65536', v6: '64' };
		expect(poolInputs({})).toEqual(want);
		expect(
			poolInputs({
				'app.addressPool.ttlClamp': null,
				'app.addressPool.maxNames': '',
				'app.addressPool.v6.chunk': null,
			}),
		).toEqual(want);
	});

	it('keeps a value the daemon sent', () => {
		expect(
			poolInputs({
				'app.addressPool.ttlClamp': 120,
				'app.addressPool.maxNames': '1024',
				'app.addressPool.v6.chunk': 0,
			}),
		).toEqual({ ttl: '120', max: '1024', v6: '0' });
	});
});
