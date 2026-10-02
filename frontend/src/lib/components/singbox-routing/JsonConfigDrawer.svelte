<script lang="ts">
	import { SideDrawer, Button } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { downloadSingboxConfigText } from '$lib/utils/singboxConfigExport';
	import { notifications } from '$lib/stores/notifications';
	import { m } from '$lib/i18n';

	interface Props {
		open: boolean;
		onClose: () => void;
	}

	let { open, onClose }: Props = $props();

	let json = $state<string>('');
	let loading = $state(false);
	let error = $state<string | null>(null);
	let copied = $state(false);
	let lastLoadedFor = $state(false);

	async function load() {
		loading = true;
		error = null;
		try {
			const res = await api.singboxGetConfigPreview();
			json = res.json;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
			json = '';
		} finally {
			loading = false;
		}
	}

	async function onCopy() {
		if (!json) return;
		const ok = await copyToClipboard(json);
		if (ok) {
			copied = true;
			notifications.success(m.singbox_routing_json_copied_toast());
			setTimeout(() => {
				copied = false;
			}, 1500);
		} else {
			notifications.error(m.singbox_routing_slot_copy_failed());
		}
	}

	function onExport() {
		if (!json) return;
		try {
			downloadSingboxConfigText(json);
			notifications.success(m.singbox_routing_json_downloaded_toast());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : String(e));
		}
	}

	$effect(() => {
		if (open && !lastLoadedFor) {
			lastLoadedFor = true;
			load();
		} else if (!open && lastLoadedFor) {
			lastLoadedFor = false;
		}
	});
</script>

<SideDrawer {open} {onClose} title={m.singbox_routing_json_title()} width={720}>
	<div class="content">
		<div class="toolbar">
			<Button variant="secondary" size="sm" onclick={load} disabled={loading}>
				{loading ? m.routing_singbox_loading() : m.routing_page_refresh()}
			</Button>
			<Button
				variant="secondary"
				size="sm"
				onclick={onCopy}
				disabled={loading || !json}
			>
				{copied ? m.servers_conf_copied() : m.diag_logs_copy()}
			</Button>
			<Button
				variant="secondary"
				size="sm"
				onclick={onExport}
				disabled={loading || !json}
			>
				{m.singbox_routing_json_export()}
			</Button>
		</div>

		<div class="state">
			{#if loading && !json}
				<div class="placeholder">{m.singbox_routing_json_loading()}</div>
			{:else if error}
				<div class="error">
					<div class="error-title">{m.singbox_routing_json_load_failed()}</div>
					<div class="error-message">{error}</div>
				</div>
			{:else if json}
				<pre class="json">{json}</pre>
			{:else}
				<div class="placeholder">{m.singbox_routing_json_empty()}</div>
			{/if}
		</div>
	</div>
</SideDrawer>

<style>
	.content {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		height: 100%;
	}

	.toolbar {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}

	@media (max-width: 640px) {
		.toolbar {
			display: grid;
			grid-template-columns: repeat(3, minmax(0, 1fr));
			align-items: stretch;
		}

		.toolbar :global(.btn) {
			width: 100%;
			min-width: 0;
		}
	}

	.state {
		flex: 1;
		min-height: 0;
		display: flex;
		flex-direction: column;
	}

	.placeholder {
		padding: 1.5rem;
		text-align: center;
		color: var(--color-text-secondary);
		font-size: 13px;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
	}

	.error {
		padding: 0.875rem 1rem;
		border: 1px solid var(--color-error);
		background: color-mix(in srgb, var(--color-error) 12%, transparent);
		border-radius: var(--radius-sm);
		color: var(--color-text-primary);
	}

	.error-title {
		font-weight: 600;
		font-size: 13px;
		margin-bottom: 0.25rem;
	}

	.error-message {
		font-size: 12px;
		color: var(--color-text-secondary);
		word-break: break-word;
	}

	.json {
		flex: 1;
		margin: 0;
		padding: 0.875rem 1rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, 'Liberation Mono', monospace;
		font-size: 12px;
		line-height: 1.5;
		color: var(--color-text-primary);
		overflow: auto;
		white-space: pre;
		tab-size: 2;
	}
</style>
