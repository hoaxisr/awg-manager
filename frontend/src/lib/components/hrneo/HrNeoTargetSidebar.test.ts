import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import HrNeoTargetSidebar from './HrNeoTargetSidebar.svelte';

// Enter/пробел на «вверх/вниз» — нажатие кнопки, а не выбор строки цели:
// обработчик строки не должен перехватывать (preventDefault) их клавиши.
describe('HrNeoTargetSidebar: клавиатура на кнопках порядка', () => {
	function setup() {
		const onselect = vi.fn();
		const { container } = render(HrNeoTargetSidebar, {
			props: {
				targets: [
					{ name: 'a', kind: 'policy', ruleCount: 1 },
					{ name: 'b', kind: 'policy', ruleCount: 2 }
				],
				selected: null,
				geoSiteCount: 0,
				geoIPCount: 0,
				oversizedCount: 0,
				onselect,
				onreorder: () => {},
				onnewrule: () => {}
			}
		});
		const row = container.querySelector('.row[role="button"]') as HTMLElement;
		const arrow = row.querySelector('.arrow-btn:not([disabled])') as HTMLButtonElement;
		return { onselect, row, arrow };
	}

	it('Enter на стрелке не выбирает строку и не гасит нажатие кнопки', async () => {
		const { onselect, arrow } = setup();
		expect(await fireEvent.keyDown(arrow, { key: 'Enter' })).toBe(true);
		expect(await fireEvent.keyDown(arrow, { key: ' ' })).toBe(true);
		expect(onselect).not.toHaveBeenCalled();
	});

	it('Enter на самой строке выбирает цель', async () => {
		const { onselect, row } = setup();
		expect(await fireEvent.keyDown(row, { key: 'Enter' })).toBe(false);
		expect(onselect).toHaveBeenCalledWith({ type: 'target', name: 'a' });
	});
});

describe('HrNeoTargetSidebar: пустая цель', () => {
	it('показывает метку «нет групп» и счётчик 0', () => {
		const { container } = render(HrNeoTargetSidebar, {
			props: {
				targets: [{ name: 'Belka', kind: 'policy', ruleCount: 0, empty: true }],
				selected: null,
				geoSiteCount: 0,
				geoIPCount: 0,
				oversizedCount: 0,
				onselect: () => {},
				onreorder: () => {},
				onnewrule: () => {}
			}
		});
		const row = container.querySelector('.row[role="button"]') as HTMLElement;
		expect(row.textContent).toContain('0');
		expect(row.querySelector('.empty-badge')).not.toBeNull();
	});
});
