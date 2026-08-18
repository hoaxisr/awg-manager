<script lang="ts">
	import { onMount, untrack } from 'svelte';
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
		wgConf?: string;
		keeneticSelected?: boolean;
		clientListenPort?: number;
		autoApply?: boolean;
		compact?: boolean;
		/** Подпись выпадающего списка; деталь «Раздача» ставит свою (SH-60). */
		peerLabel?: string;
	}

	let {
		onConnect,
		onPeerConf,
		wgConf = $bindable(''),
		keeneticSelected = $bindable(false),
		clientListenPort = 9000,
		autoApply = false,
		compact = false,
		peerLabel = 'Сервер · пир'
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

	const keeneticPeer = $derived.by(() => {
		if (!selected || !snap) return false;
		try {
			const { kind, serverId, pubkey } = decodeServerPeerValue(selected);
			if (kind !== 'system') return false;
			const srv = snap.servers?.find((s) => s.id === serverId);
			const peer = srv?.peers?.find((p) => p.publicKey === pubkey);
			return peer?.confAvailable !== true;
		} catch {
			return false;
		}
	});

	$effect(() => {
		keeneticSelected = keeneticPeer;
	});

	onMount(() => {
		const unsub = servers.subscribe((st) => {
			snap = st.data;
		});
		void servers.refetch();
		return unsub;
	});

	function applyManualWgConf() {
		const raw = wgConf.trim();
		if (!raw) {
			onPeerConf('');
			return;
		}
		onPeerConf(patchWgConfEndpoint(raw, endpointPort));
	}

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

			if (kind === 'system') {
				const srv = snap.servers?.find((s) => s.id === serverId);
				const peer = srv?.peers?.find((p) => p.publicKey === pubkey);
				if (peer?.confAvailable !== true) {
					wgConf = '';
					onPeerConf('');
					return;
				}
			}

			const conf =
				kind === 'system'
					? await api.getSystemServerPeerConf(serverId, pubkey)
					: await api.getManagedPeerConf(serverId, pubkey);
			wgConf = conf;
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

	$effect(() => {
		if (!keeneticPeer) return;
		const raw = wgConf;
		untrack(() => {
			if (!raw.trim()) {
				onPeerConf('');
				return;
			}
			onPeerConf(patchWgConfEndpoint(raw.trim(), endpointPort));
		});
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
			label={peerLabel}
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

	{#if keeneticPeer}
		<div class="ft-keenetic-warn">
			Пир создан в Keenetic OS — приватный ключ недоступен через API. Вставьте WG-конфиг клиента
			(<code>[Interface]</code> / <code>[Peer]</code>) в поле ниже.
		</div>
		<label class="ft-wg-conf-label" for="ft-wg-conf-manual">WG-конфиг клиента</label>
		<textarea
			id="ft-wg-conf-manual"
			class="ft-wg-conf"
			bind:value={wgConf}
			oninput={applyManualWgConf}
			rows="8"
			placeholder="[Interface]&#10;PrivateKey = …&#10;Address = …&#10;&#10;[Peer]&#10;PublicKey = …&#10;Endpoint = 127.0.0.1:9000"
		></textarea>
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

	.ft-keenetic-warn {
		margin-top: 0.875rem;
		padding: 0.625rem 0.75rem;
		border-radius: var(--radius-sm);
		border: 1px solid color-mix(in srgb, var(--color-warning, #d97706) 45%, var(--color-border));
		background: color-mix(in srgb, var(--color-warning, #d97706) 8%, var(--color-bg-primary));
		color: var(--color-text-primary);
		font-size: 1rem;
		line-height: 1.45;
	}

	.ft-wg-conf-label {
		display: block;
		margin-top: 0.75rem;
		margin-bottom: 0.375rem;
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}

	.ft-wg-conf {
		width: 100%;
		min-height: 9rem;
		padding: 0.625rem 0.75rem;
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		line-height: 1.45;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		resize: vertical;
	}

	.ft-err {
		color: var(--color-error);
		font-size: 0.8125rem;
		margin-top: 0.5rem;
	}
</style>
