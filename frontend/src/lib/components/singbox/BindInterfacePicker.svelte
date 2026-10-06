<script lang="ts">
	import { m } from '$lib/i18n';
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { Dropdown, type DropdownOption } from '$lib/components/ui';
	import type { SingboxRouterWANInterface } from '$lib/types';
	import { bindInterfaceLabel } from '$lib/utils/bindInterface';

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
		label,
		value = $bindable(''),
		hint,
		disabled = false,
		onchange
	}: Props = $props();

	let bindables = $state<SingboxRouterWANInterface[]>([]);
	let loading = $state(true);
	let loadError = $state<string | null>(null);
	const error = $derived(loadError === null ? '' : loadError || m.singbox_bind_load_failed());

	onMount(() => {
		void api
			.singboxRouterListBindableInterfaces()
			.then((list) => {
				bindables = list;
			})
			.catch((err) => {
				bindables = [];
				loadError = err.message || '';
			})
			.finally(() => {
				loading = false;
			});
	});

	const options = $derived<DropdownOption[]>([
		{ value: '', label: m.singbox_bind_default_auto() },
		...bindables.map((i) => ({
			value: i.name,
			label: bindInterfaceLabel(i)
		}))
	]);
</script>

<Dropdown
	{id}
	label={label ?? m.singbox_bind_label()}
	bind:value
	{options}
	{disabled}
	hint={hint ?? m.singbox_bind_hint()}
	fullWidth
	placeholder={loading ? m.singbox_bind_loading() : m.singbox_bind_default()}
	error={error}
	onchange={(v) => onchange?.(v)}
/>
