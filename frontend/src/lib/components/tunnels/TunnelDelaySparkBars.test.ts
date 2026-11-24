import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render } from '@testing-library/svelte';
import TunnelDelaySparkBars from './TunnelDelaySparkBars.svelte';

// F875: клик мышью не должен передавать MouseEvent в колбэк — у вызывающих
// первый параметр значимый (triggerCheck(silent)), событие делало проверку «тихой».
describe('TunnelDelaySparkBars', () => {
	it('click and Enter call onclick without arguments', async () => {
		const onclick = vi.fn();
		const { getByRole } = render(TunnelDelaySparkBars, { props: { history: [], state: 'stopped', onclick } });
		const el = getByRole('button');
		await fireEvent.click(el);
		await fireEvent.keyDown(el, { key: 'Enter' });
		expect(onclick).toHaveBeenCalledTimes(2);
		for (const call of onclick.mock.calls) expect(call).toEqual([]);
	});
});
