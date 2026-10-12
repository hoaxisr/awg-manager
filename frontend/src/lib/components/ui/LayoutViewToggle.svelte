<script lang="ts" generics="T extends LayoutViewMode">
	import { m } from '$lib/i18n';
	import SegmentedControl from './SegmentedControl.svelte';
	import type { SegmentedOption } from './segmentedControl';
	import type { LayoutViewDense, LayoutViewMode } from './layoutViewToggle';

	interface Props {
		value: T;
		onchange: (next: T) => void;
		ariaLabel?: string;
		/** Значение сегмента «мелкая сетка»: AWG — `cards`, sing-box — `dense`. */
		denseValue?: LayoutViewDense;
		/** Скрыть «список» (базовый уровень, вкладка участников подписки). */
		showListOption?: boolean;
		/** Скрыть «мелкую сетку» (вкладка участников подписки). */
		showDenseOption?: boolean;
	}

	let {
		value,
		onchange,
		ariaLabel,
		denseValue = 'dense',
		showListOption = true,
		showDenseOption = true,
	}: Props = $props();

	const options = $derived.by((): SegmentedOption<T>[] => {
		const items: SegmentedOption<T>[] = [];
		if (showDenseOption) {
			items.push({ value: denseValue as T, label: m.ui_layout_toggle_dense(), icon: 'dense' });
		}
		items.push({ value: 'compact' as T, label: m.ui_layout_toggle_grid(), icon: 'compact' });
		if (showListOption) {
			items.push({ value: 'list' as T, label: m.ui_layout_toggle_list(), icon: 'list' });
		}
		return items;
	});
</script>

<SegmentedControl variant="icon" {value} {options} ariaLabel={ariaLabel ?? m.ui_layout_toggle_aria()} onchange={onchange} />
