<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import SingboxSettingsModal from './SingboxSettingsModal.svelte';
	import type { SingboxRouterDNSRewrite } from '$lib/types';

	interface Props {
		rewrite?: SingboxRouterDNSRewrite;
		onClose: () => void;
		onSave: (rewrite: SingboxRouterDNSRewrite) => Promise<void> | void;
	}
	let { rewrite, onClose, onSave }: Props = $props();

	// svelte-ignore state_referenced_locally
	let pattern = $state(rewrite?.pattern ?? '');
	// svelte-ignore state_referenced_locally
	let ipsStr = $state((rewrite?.ips ?? []).join(', '));
	let busy = $state(false);
	let error = $state('');
	let errorKind = $state<'' | 'pattern' | 'ips'>('');
	const errorText = $derived(
		error ||
			(errorKind === 'pattern'
				? m.routing_singbox_rewrite_pattern_required()
				: errorKind === 'ips'
					? m.routing_singbox_rewrite_ip_required()
					: ''),
	);

	async function save(): Promise<void> {
		busy = true;
		error = '';
		errorKind = '';
		try {
			const p = pattern.trim();
			if (!p) { errorKind = 'pattern'; busy = false; return; }
			const ips = ipsStr.split(',').map((s) => s.trim()).filter(Boolean);
			if (ips.length === 0) { errorKind = 'ips'; busy = false; return; }
			await onSave({ pattern: p, ips });
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<SingboxSettingsModal
	title={rewrite ? m.routing_singbox_rewrite_edit_title() : m.routing_singbox_rewrite_new_title()}
	onClose={onClose}
	size="md"
>
	<div class="form">
		<label class="field">
			<div class="lbl">{m.routing_singbox_rewrite_pattern_label()}</div>
			<input class="mono" bind:value={pattern} placeholder="nas.lan · *.discord.media · finland10*.discord.media" />
			<div class="hint">
				{m.routing_singbox_rewrite_hint_without()} <code>*</code> {m.routing_singbox_rewrite_hint_exact()} <code>*.suffix</code> {m.routing_singbox_rewrite_hint_subdomains()}
				<code>prefix*.suffix</code> {m.routing_singbox_rewrite_hint_wildcard_pre()} <code>*</code>{m.routing_singbox_rewrite_hint_wildcard_post()}
			</div>
		</label>
		<label class="field">
			<div class="lbl">{m.routing_singbox_rewrite_ips_label()}</div>
			<input class="mono" bind:value={ipsStr} placeholder="104.25.158.178, fd00::5" />
		</label>
		{#if errorText}<div class="error">{errorText}</div>{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onClose} type="button">{m.common_cancel()}</Button>
		<Button variant="primary" size="md" onclick={save} disabled={busy} loading={busy} type="button">
			{m.common_save()}
		</Button>
	{/snippet}
</SingboxSettingsModal>

<style>
	.mono {
		font-family: ui-monospace, monospace;
	}
</style>
