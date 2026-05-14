<script lang="ts">
	import { api } from '$lib/api/client';
	import { Dropdown, type DropdownOption } from '$lib/components/ui';
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

	const STRATEGY_OPTIONS: DropdownOption<SingboxRouterDNSStrategy>[] = [
		{ value: '', label: '— default —' },
		{ value: 'ipv4_only', label: 'ipv4_only' },
		{ value: 'ipv6_only', label: 'ipv6_only' },
		{ value: 'prefer_ipv4', label: 'prefer_ipv4' },
		{ value: 'prefer_ipv6', label: 'prefer_ipv6' },
	];

	const finalServerOptions = $derived<DropdownOption[]>([
		{ value: '', label: '— не задан —' },
		...servers.map((s) => ({ value: s.tag, label: s.tag })),
	]);

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
			<Dropdown bind:value={draftFinal} options={finalServerOptions} disabled={servers.length === 0} fullWidth />
			<div class="hint">Сервер по умолчанию для запросов, не попавших ни под одно правило.</div>
		</label>
		<label class="field">
			<div class="lbl">Стратегия (глобальная)</div>
			<Dropdown bind:value={draftStrategy} options={STRATEGY_OPTIONS} fullWidth />
			<div class="hint">Для роутера без IPv6 — <code>ipv4_only</code>.</div>
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
