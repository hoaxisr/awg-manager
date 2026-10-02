<script lang="ts">
	import { m } from '$lib/i18n';
	import { Dropdown, type DropdownOption } from '$lib/components/ui';

	interface Props {
		search: string;
		fTunnel: string;
		fProto: string;
		fState: string;
		tunnelOptions: DropdownOption[];
		onSearchInput: (v: string) => void;
		onTunnel: (v: string) => void;
		onProto: (v: string) => void;
		onState: (v: string) => void;
	}

	let { search, fTunnel, fProto, fState, tunnelOptions, onSearchInput, onTunnel, onProto, onState }: Props =
		$props();

	const PROTO_OPTIONS = $derived<DropdownOption[]>([
		{ value: 'all', label: m.diag_conn_filter_all() },
		{ value: 'tcp', label: 'TCP' },
		{ value: 'udp', label: 'UDP' },
		{ value: 'icmp', label: 'ICMP' },
	]);
	const STATE_OPTIONS = $derived<DropdownOption[]>([
		{ value: 'all', label: m.diag_conn_filter_all() },
		{ value: 'ESTABLISHED', label: 'ESTABLISHED' },
		{ value: 'SYN_SENT', label: 'SYN_SENT' },
		{ value: 'TIME_WAIT', label: 'TIME_WAIT' },
	]);

	const chips = $derived.by(() => {
		const list: { id: string; label: string; clear: () => void }[] = [];
		if (fTunnel !== 'all') {
			const label = tunnelOptions.find((o) => o.value === fTunnel)?.label ?? fTunnel;
			list.push({ id: 'tunnel', label, clear: () => onTunnel('all') });
		}
		if (fProto !== 'all') list.push({ id: 'proto', label: fProto.toUpperCase(), clear: () => onProto('all') });
		if (fState !== 'all') list.push({ id: 'state', label: fState, clear: () => onState('all') });
		if (search) list.push({ id: 'search', label: `«${search}»`, clear: () => onSearchInput('') });
		return list;
	});

	function clearAll(): void {
		onTunnel('all');
		onProto('all');
		onState('all');
		onSearchInput('');
	}
</script>

<div class="filter-card">
	<input
		type="search"
		class="field-input compact"
		placeholder={m.connections_search_placeholder()}
		value={search}
		oninput={(e) => onSearchInput(e.currentTarget.value)}
	/>
	<div class="fields">
		<Dropdown label={m.connections_filter_tunnel()} value={fTunnel} options={tunnelOptions} onchange={onTunnel} fullWidth />
		<Dropdown label={m.connections_filter_protocol()} value={fProto} options={PROTO_OPTIONS} onchange={onProto} fullWidth />
		<Dropdown label={m.connections_filter_state()} value={fState} options={STATE_OPTIONS} onchange={onState} fullWidth />
	</div>
	{#if chips.length > 0}
		<div class="chips-row">
			{#each chips as chip (chip.id)}
				<button type="button" class="fchip" onclick={chip.clear}>{chip.label} ×</button>
			{/each}
			<button type="button" class="clear-all" onclick={clearAll}>{m.connections_clear_all()}</button>
		</div>
	{/if}
</div>

<style>
	.filter-card {
		padding: 12px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		display: grid;
		gap: 10px;
		margin-bottom: 12px;
	}
	.fields {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 10px;
	}
	@media (max-width: 760px) {
		.fields { grid-template-columns: 1fr; }
	}
	.chips-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 6px;
	}
	.fchip {
		border: 1px solid color-mix(in srgb, var(--color-accent) 35%, transparent);
		background: color-mix(in srgb, var(--color-accent) 15%, transparent);
		color: var(--color-accent);
		border-radius: 999px;
		padding: 3px 10px;
		font-size: 11px;
		cursor: pointer;
	}
	.clear-all {
		border: none;
		background: none;
		color: var(--color-text-muted);
		font-size: 11px;
		cursor: pointer;
		text-decoration: underline;
	}
	.clear-all:hover { color: var(--color-text-primary); }
</style>
