<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { Dropdown, type DropdownOption } from '$lib/components/ui';
	import type { SingboxRouterWANInterface } from '$lib/types';

	interface Props {
		id?: string;
		label?: string;
		value?: string;
		hint?: string;
		disabled?: boolean;
		onchange?: (value: string) => void;
	}

	let {
		id = 'bind-interface',
		label = 'Исходящий интерфейс',
		value = $bindable(''),
		hint = 'Принудительно направляет dial этого прокси через выбранный uplink (модем, WAN, Wi‑Fi). Пусто — маршрут по умолчанию.',
		disabled = false,
		onchange
	}: Props = $props();

	let bindables = $state<SingboxRouterWANInterface[]>([]);
	let loading = $state(true);

	onMount(() => {
		void api
			.singboxRouterListBindableInterfaces()
			.then((list) => {
				bindables = list;
			})
			.catch(() => {
				bindables = [];
			})
			.finally(() => {
				loading = false;
			});
	});

	const options = $derived<DropdownOption[]>([
		{ value: '', label: '— по умолчанию (auto) —' },
		...bindables.map((i) => ({
			value: i.name,
			label: `${i.label} · ${i.name}${i.up ? '' : ' (down)'}`
		}))
	]);
</script>

<Dropdown
	{id}
	{label}
	bind:value
	{options}
	{disabled}
	fullWidth
	placeholder={loading ? 'Загрузка интерфейсов…' : '— по умолчанию —'}
	onchange={(v) => onchange?.(v)}
/>
{#if hint}
	<p class="bind-hint">{hint}</p>
{/if}

<style>
	.bind-hint {
		margin: 6px 0 0;
		font-size: 12px;
		line-height: 1.45;
		color: var(--text-muted);
	}
</style>
