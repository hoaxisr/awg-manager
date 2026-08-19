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
	let error = $state('');

	onMount(() => {
		void api
			.singboxRouterListBindableInterfaces('all')
			.then((list) => {
				bindables = list;
			})
			.catch((err) => {
				bindables = [];
				error = err.message || 'Ошибка загрузки списка интерфейсов';
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
	{hint}
	fullWidth
	placeholder={loading ? 'Загрузка интерфейсов…' : '— по умолчанию —'}
	error={error}
	onchange={(v) => onchange?.(v)}
/>
