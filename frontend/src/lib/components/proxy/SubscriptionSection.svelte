<script lang="ts">
	// EX-30..33 — «Подписка». «Обновить список» обновляет параметры ТЕКУЩЕГО
	// peer (ручка бэкенда), а смена сервера внутри подписки — это повторный
	// импорт профиля, поэтому он и живёт под «Применить и запустить»
	// (оговорка факт-чека EX-31/32/33).
	import { onMount } from 'svelte';
	import { Button } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errText } from '$lib/utils/errorMessage';
	import { peersEqual } from '$lib/utils/wdttPeer';
	import { setPeer } from '$lib/utils/wdttPeerMode';
	import type { WdttClientConfig, WdttImportPayload } from '$lib/types';
	import DetailSection from './DetailSection.svelte';

	interface Props {
		instanceId: string;
		/** Конфиг инстанса — профиль подписки применяется прямо в него. */
		client: WdttClientConfig;
		/** Сохранить конфиг и запустить инстанс (владелец — страница). */
		onsaveandstart: () => Promise<void> | void;
		/** Перечитать конфиги после ручки обновления подписки. */
		onreload: () => Promise<void> | void;
	}

	let { instanceId, client, onsaveandstart, onreload }: Props = $props();

	let profiles = $state<WdttImportPayload[]>([]);
	let selected = $state(0);
	let refreshing = $state(false);
	let applying = $state(false);

	const applied = $derived(
		!!profiles[selected]?.peer && peersEqual(profiles[selected].peer, client.peer),
	);

	function profileLabel(p: WdttImportPayload, idx: number): string {
		const name = p.name?.trim() || String(idx + 1);
		const host = p.peer?.split(':')[0] ?? '';
		return host ? `${name} (${host})` : name;
	}

	async function loadProfiles() {
		const url = client.sub?.trim();
		if (!url) return;
		try {
			const decoded = await api.decodeWdttLink(url);
			profiles = decoded.subscription?.profiles ?? [];
			const idx = profiles.findIndex((p) => peersEqual(p.peer, client.peer));
			if (idx >= 0) selected = idx;
		} catch (e) {
			notifications.error(errText(e));
		}
	}

	onMount(loadProfiles);

	async function refresh() {
		refreshing = true;
		try {
			const result = await api.refreshWdttSubscription(instanceId);
			await onreload();
			await loadProfiles();
			if (result.message) notifications.success(result.message);
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			refreshing = false;
		}
	}

	async function applyAndStart() {
		const payload = profiles[selected];
		if (!payload) return;
		applying = true;
		try {
			if (payload.peer) setPeer(client, payload.peer);
			if (payload.password) client.password = payload.password;
			if (payload.vkHashes?.length) client.vkHashes = payload.vkHashes.join(',');
			if (payload.workers && payload.workers > 0) client.workers = payload.workers;
			await onsaveandstart();
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			applying = false;
		}
	}
</script>

<DetailSection title="Подписка">
	<p class="line"><code>{client.sub}</code></p>
	{#if profiles.length}
		<select class="profile-select" bind:value={selected} aria-label="Подписка">
			{#each profiles as p, idx (idx)}
				<option value={idx}>{profileLabel(p, idx)}</option>
			{/each}
		</select>
		{#if applied}
			<p class="line">Этот профиль уже применён</p>
		{/if}
	{/if}
	<div class="btn-row">
		<Button variant="secondary" loading={refreshing} onclick={refresh}>Обновить список</Button>
		<Button
			variant="primary"
			loading={applying}
			disabled={!profiles.length}
			onclick={applyAndStart}
		>
			Применить и запустить
		</Button>
	</div>
</DetailSection>

<style>
	.line {
		margin: 0 0 0.5rem;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	code {
		font-family: var(--font-mono);
		font-size: 0.78em;
		background: var(--color-bg-tertiary);
		padding: 0.05rem 0.3rem;
		border-radius: 4px;
		word-break: break-all;
	}

	.profile-select {
		width: 100%;
		padding: 0.5rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
	}

	.btn-row {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
		margin-top: 0.75rem;
	}
</style>
