<script lang="ts">
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

	async function save(): Promise<void> {
		busy = true;
		error = '';
		try {
			const p = pattern.trim();
			if (!p) { error = 'Шаблон обязателен'; busy = false; return; }
			const ips = ipsStr.split(',').map((s) => s.trim()).filter(Boolean);
			if (ips.length === 0) { error = 'Укажите хотя бы один IP'; busy = false; return; }
			await onSave({ pattern: p, ips });
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<SingboxSettingsModal
	title={rewrite ? 'Редактировать перезапись' : 'Новая перезапись'}
	onClose={onClose}
	size="md"
>
	<div class="form">
		<label class="field">
			<div class="lbl">Шаблон домена</div>
			<input class="mono" bind:value={pattern} placeholder="nas.lan · *.discord.media · finland10*.discord.media" />
			<div class="hint">
				Без <code>*</code> — точный домен. <code>*.suffix</code> — все поддомены.
				<code>prefix*.suffix</code> — wildcard внутри первой метки (нужен доменный хвост после <code>*</code>).
			</div>
		</label>
		<label class="field">
			<div class="lbl">IP-адреса (через запятую)</div>
			<input class="mono" bind:value={ipsStr} placeholder="104.25.158.178, fd00::5" />
		</label>
		{#if error}<div class="error">{error}</div>{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onClose} type="button">Отмена</Button>
		<Button variant="primary" size="md" onclick={save} disabled={busy} loading={busy} type="button">
			Сохранить
		</Button>
	{/snippet}
</SingboxSettingsModal>

<style>
	.mono {
		font-family: ui-monospace, monospace;
	}
</style>
