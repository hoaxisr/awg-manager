<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type {
		SingboxRouterDNSServer,
		SingboxRouterDNSGlobals,
		SingboxRouterDNSStrategy,
	} from '$lib/types';

	interface Props {
		globals: SingboxRouterDNSGlobals;
		servers: SingboxRouterDNSServer[];
		onChange: () => Promise<void> | void;
	}
	let { globals, servers, onChange }: Props = $props();

	let final = $derived(globals.final);
	let strategy = $derived(globals.strategy);

	let draftFinal = $state('');
	let draftStrategy = $state<SingboxRouterDNSStrategy>('');
	let busy = $state(false);

	$effect(() => {
		draftFinal = globals.final;
		draftStrategy = globals.strategy;
	});

	const dirty = $derived(draftFinal !== final || draftStrategy !== strategy);

	async function save(): Promise<void> {
		busy = true;
		try {
			await api.singboxRouterPutDNSGlobals({ final: draftFinal, strategy: draftStrategy });
			await onChange();
		} catch (e) {
			notifications.error((e as Error).message);
		} finally {
			busy = false;
		}
	}
</script>

<div class="card">
	<div class="title">Общие настройки DNS</div>
	<div class="row-2">
		<label class="field">
			<div class="lbl">Final сервер</div>
			<select bind:value={draftFinal} disabled={servers.length === 0}>
				<option value="">— не задан —</option>
				{#each servers as s}
					<option value={s.tag}>{s.tag}</option>
				{/each}
			</select>
			<div class="hint">Сервер по умолчанию для запросов, не попавших ни под одно правило.</div>
		</label>
		<label class="field">
			<div class="lbl">Стратегия (глобальная)</div>
			<select bind:value={draftStrategy}>
				<option value="">— default —</option>
				<option value="ipv4_only">ipv4_only</option>
				<option value="ipv6_only">ipv6_only</option>
				<option value="prefer_ipv4">prefer_ipv4</option>
				<option value="prefer_ipv6">prefer_ipv6</option>
			</select>
			<div class="hint">Для Keenetic без IPv6 — <code>ipv4_only</code>.</div>
		</label>
	</div>
	<div class="actions">
		<button class="btn btn-primary" onclick={save} disabled={busy || !dirty} type="button">
			Сохранить
		</button>
	</div>
</div>

<style>
	.card {
		background: var(--surface-bg);
		padding: 0.8rem 1rem;
		border-radius: 6px;
		margin-bottom: 1rem;
	}
	.title {
		font-size: 0.8rem;
		font-weight: 600;
		margin-bottom: 0.6rem;
		color: var(--text);
	}
	.row-2 {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.75rem;
	}
	.field {
		display: grid;
		gap: 0.25rem;
	}
	.lbl {
		font-size: 0.75rem;
		color: var(--muted-text);
	}
	.field select {
		background: var(--bg);
		border: 1px solid var(--border);
		padding: 0.4rem 0.6rem;
		border-radius: 4px;
		color: var(--text);
		font-family: ui-monospace, monospace;
		font-size: 0.85rem;
	}
	.hint {
		font-size: 0.72rem;
		color: var(--muted-text);
		line-height: 1.3;
	}
	.hint code {
		background: var(--bg);
		padding: 0.05rem 0.25rem;
		border-radius: 2px;
		font-family: ui-monospace, monospace;
	}
	.actions {
		margin-top: 0.75rem;
		display: flex;
		justify-content: flex-end;
	}
</style>
