<script lang="ts">
	// Быстрая смена выхода в интернет для Telegram. Сохранение — здесь, у формы;
	// владелец получает новый конфиг через onsaved.
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { TgWebProxyConfig, TelegramUpstream } from '$lib/types';
	import { Button, Modal } from '$lib/components/ui';
	import TelegramUpstreamsEditor from '../TelegramUpstreamsEditor.svelte';
	import {
		buildEditableUpstreams,
		cleanUpstreams,
		primaryDirectDevice,
		upstreamsDraftError,
		type EgressChoice,
	} from './tgProxyModel';

	interface Props {
		config: TgWebProxyConfig | null;
		egressOptions: EgressChoice[];
		currentEgressUnavailable: boolean;
		onsaved: (config: TgWebProxyConfig) => void | Promise<void>;
	}

	let { config, egressOptions, currentEgressUnavailable, onsaved }: Props = $props();

	let open = $state(false);
	let quickUpstreams = $state<TelegramUpstream[]>([]);
	let quickEgressSaving = $state(false);

	export function show() {
		quickUpstreams = buildEditableUpstreams(config);
		open = true;
	}

	async function saveQuickEgress() {
		if (!config) return;
		const draftError = upstreamsDraftError(quickUpstreams);
		if (draftError) {
			notifications.error(draftError);
			return;
		}
		quickEgressSaving = true;
		try {
			const clean = cleanUpstreams(quickUpstreams);
			const saved = await api.updateTgWebProxyConfig({
				...config,
				upstreams: clean,
				upstream_device: primaryDirectDevice(clean),
			});
			notifications.success(m.servers_tg_egress_saved());
			open = false;
			await onsaved(saved);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_tg_egress_save_error());
		} finally {
			quickEgressSaving = false;
		}
	}
</script>

<Modal bind:open title={m.servers_tg_egress_title()} onclose={() => (open = false)} size="md">
	<div class="egress-modal-body">
		<p class="field-hint">
			{m.servers_tg_egress_hint()}
		</p>
		<TelegramUpstreamsEditor bind:upstreams={quickUpstreams} {egressOptions} />
		{#if currentEgressUnavailable}
			<div class="field-warn">
				{m.servers_tg_egress_current_unavailable()}
			</div>
		{/if}
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (open = false)}>{m.common_cancel()}</Button>
		<Button variant="primary" onclick={saveQuickEgress} disabled={quickEgressSaving}>
			{quickEgressSaving ? m.common_saving() : m.common_save()}
		</Button>
	{/snippet}
</Modal>

<style>
	.egress-modal-body {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.field-hint {
		font-size: 11.5px;
		color: var(--color-text-muted);
		margin: 0;
	}

	.field-warn {
		font-size: 12px;
		color: var(--color-warning);
		margin-top: 6px;
	}
</style>
