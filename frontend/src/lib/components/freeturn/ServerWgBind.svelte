<script lang="ts">
	import { onMount } from 'svelte';
	import { Dropdown, Button, Input } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { servers, type ServersSnapshot } from '$lib/stores/servers';
	import {
		buildRunningServerPeerDropdownOptions,
		decodeServerPeerValue,
		patchWgConfEndpoint,
		resolveServerListenPort,
		wgEndpointHint
	} from '$lib/utils/serverPeerOptions';

	interface Props {
		onConnect: (addr: string) => void;
		onPeerConf: (conf: string) => void;
		clientListenPort?: number;
		/** В простом режиме: применить сразу при выборе пира */
		autoApply?: boolean;
		/** Компактный вид без лишних рамок */
		compact?: boolean;
	}

	let {
		onConnect,
		onPeerConf,
		clientListenPort = 9000,
		autoApply = false,
		compact = false
	}: Props = $props();

	let snap: ServersSnapshot | null = $state(null);
	let selected = $state('');
	let loading = $state(false);
	let error = $state('');
	let endpointPort = $state(9000);

	$effect(() => {
		endpointPort = clientListenPort;
	});

	const options = $derived(buildRunningServerPeerDropdownOptions(snap));
	const endpointHint = $derived(wgEndpointHint(endpointPort));

	onMount(() => {
		const unsub = servers.subscribe((st) => {
			snap = st.data;
		});
		void servers.refetch();
		return unsub;
	});

	async function apply() {
		if (!selected || !snap) return;
		error = '';
		loading = true;
		try {
			const { kind, serverId, pubkey } = decodeServerPeerValue(selected);
			const port = resolveServerListenPort(snap, kind, serverId);
			if (!port) {
				error = 'Не удалось определить listenPort сервера';
				return;
			}
			onConnect(`127.0.0.1:${port}`);

			const conf =
				kind === 'system'
					? await api.getSystemServerPeerConf(serverId, pubkey)
					: await api.getManagedPeerConf(serverId, pubkey);
			onPeerConf(patchWgConfEndpoint(conf, endpointPort));
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	}

	let lastAutoSelected = $state('');
	$effect(() => {
		if (!autoApply || !selected || selected === lastAutoSelected) return;
		lastAutoSelected = selected;
		void apply();
	});
</script>

<div class="ft-wg-bind" class:ft-wg-compact={compact}>
	{#if !compact}
		<div class="section-label">Привязка к WG-серверу</div>
	{/if}
	<p class="ft-hint">
		{#if compact}
			Выберите поднятый WG-сервер и пира — адрес backend (-connect) заполнится как
			<code>127.0.0.1:&lt;listenPort&gt;</code>, конфиг пира попадёт в ссылку для клиента.
		{:else}
			Выберите поднятый WG-сервер и пира — backend (-connect) заполнится как
			<code>127.0.0.1:&lt;listenPort&gt;</code>, конфиг пира попадёт в генератор ссылки с Endpoint
			<code>{endpointHint}</code> для клиента FreeTurn.
		{/if}
	</p>
	<div class="ft-wg-row">
		<Dropdown
			label="Сервер · пир"
			bind:value={selected}
			options={options}
			placeholder={options.length ? 'Выберите…' : 'Нет поднятых WG-серверов с пирами'}
			disabled={!options.length || loading}
		/>
		{#if !autoApply}
			<Button variant="secondary" size="sm" loading={loading} disabled={!selected} onclick={apply}>
				Применить
			</Button>
		{/if}
	</div>
	{#if compact}
		<div class="ft-wg-port">
			<Input
				label="Endpoint клиента FreeTurn (порт)"
				type="number"
				value={String(endpointPort)}
				onchange={(v) => (endpointPort = Number(v) || 9000)}
			/>
			<span class="ft-hint">Порт listen клиента freeturn (вкладка «Клиент»), адрес <code>127.0.0.1</code></span>
		</div>
	{:else}
		<div class="ft-wg-port">
			<Input
				label="Endpoint клиента FreeTurn (порт)"
				type="number"
				value={String(endpointPort)}
				onchange={(v) => (endpointPort = Number(v) || 9000)}
			/>
			<span class="ft-hint">Адрес: <code>127.0.0.1</code>, порт — listen клиента freeturn (вкладка «Клиент»)</span>
		</div>
	{/if}
	{#if error}
		<p class="ft-err">{error}</p>
	{/if}
</div>

<style>
	.ft-wg-bind {
		margin-bottom: 1rem;
		padding: 0.75rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-secondary);
	}

	.ft-wg-bind.ft-wg-compact {
		margin-bottom: 0;
		padding: 0;
		border: none;
		background: transparent;
	}

	.ft-wg-row {
		display: flex;
		gap: 0.5rem;
		align-items: flex-end;
		flex-wrap: wrap;
	}

	.ft-wg-row :global(.field) {
		flex: 1;
		min-width: 12rem;
	}

	.ft-wg-port {
		margin-top: 0.75rem;
		max-width: 16rem;
	}

	.ft-hint {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		margin: 0.375rem 0 0.625rem;
	}

	.ft-err {
		color: var(--color-error);
		font-size: 0.8125rem;
		margin-top: 0.5rem;
	}
</style>
