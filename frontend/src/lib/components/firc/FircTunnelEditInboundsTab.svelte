<script lang="ts">
	import { m } from '$lib/i18n';
	import { FIRC_TUNNEL_NODE_POOL, type FircTunnel } from '$lib/types/firc';
	import { Button, IconButton } from '$lib/components/ui';
	import { Plus, Trash2 } from 'lucide-svelte';
	import type { DisplayNode } from './fircTunnelEdit';

	interface Props {
		draft: FircTunnel;
		nodes: DisplayNode[];
		onadd: () => void;
		onremove: (idx: number) => void;
	}

	let { draft = $bindable(), nodes, onadd, onremove }: Props = $props();
</script>

<div class="subtab-panel">
	<div class="sources-header">
		<span class="hint">
			{m.firc_tunnel_edit_inbounds_hint()}
		</span>
		<Button variant="secondary" size="sm" onclick={onadd}>
			{#snippet iconBefore()}<Plus size={13} />{/snippet}
			{m.firc_tunnel_edit_add_inbound()}
		</Button>
	</div>

	{#if (draft.inbounds?.length ?? 0) === 0}
		<div class="sub-empty">{m.firc_tunnel_edit_inbounds_empty()}</div>
	{:else}
		<div class="sources-list">
			{#each draft.inbounds as inb, idx (idx)}
				<div class="inbound-row">
					<div class="field">
						<label class="lbl" for={`inb-l-${idx}`}>{m.firc_tunnel_edit_inbound_listen()}</label>
						<input
							id={`inb-l-${idx}`}
							class="inp mono compact-inp"
							type="text"
							placeholder={m.firc_inbound_listen_placeholder()}
							bind:value={inb.listen}
						/>
					</div>
					<div class="field">
						<label class="lbl" for={`inb-p-${idx}`}>{m.firc_tunnel_edit_inbound_port()}</label>
						<input
							id={`inb-p-${idx}`}
							class="inp mono compact-inp"
							type="number"
							min="1"
							max="65535"
							bind:value={inb.port}
						/>
					</div>
					<div class="field">
						<label class="lbl" for={`inb-n-${idx}`}>{m.firc_tunnel_edit_inbound_node()}</label>
						<select id={`inb-n-${idx}`} class="inp compact-inp" bind:value={inb.node}>
							<option value={FIRC_TUNNEL_NODE_POOL}>{m.firc_tunnel_edit_inbound_node_pool()}</option>
							{#if inb.node === '' || inb.node === 'default'}
								<option value={inb.node}>{m.firc_tunnel_edit_inbound_node_auto()}</option>
							{/if}
							{#each nodes as n (n.key)}
								<option value={n.key}>{n.name}</option>
							{/each}
						</select>
					</div>
					<div class="inbound-del">
						<IconButton
							variant="danger"
							title={m.firc_tunnel_edit_remove_inbound()}
							ariaLabel={m.firc_tunnel_edit_remove_inbound()}
							onclick={() => onremove(idx)}
						>
							<Trash2 size={14} />
						</IconButton>
					</div>
				</div>
			{/each}
		</div>
	{/if}
</div>

<style>
	.subtab-panel {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.sources-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		flex-wrap: wrap;
	}
	.sources-list {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.lbl {
		font-size: 11.5px;
		color: var(--text-secondary);
		font-weight: 500;
	}
	.hint {
		font-size: 11px;
		color: var(--text-muted);
	}
	.inp {
		padding: 5px 8px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-primary);
		color: var(--text-primary);
		font-size: 12px;
		width: 100%;
		box-sizing: border-box;
	}
	select.inp {
		padding-right: 1.75rem;
		text-overflow: ellipsis;
	}
	.mono {
		font-family: var(--font-mono);
	}
	.inbound-row {
		display: grid;
		grid-template-columns: 1fr 90px 1.4fr auto;
		gap: 8px;
		align-items: end;
		padding: 8px 10px;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--bg-tertiary);
	}
	.inbound-row > .field {
		min-width: 0;
	}
	@media (max-width: 600px) {
		.inbound-row {
			grid-template-columns: 1fr 96px auto;
		}
		.inbound-row > .field:nth-child(3) {
			grid-column: 1 / -1;
		}
		.inbound-row > .inbound-del {
			grid-row: 1;
			grid-column: 3;
		}
	}
	.sub-empty {
		padding: 20px;
		text-align: center;
		color: var(--text-muted);
		font-size: 12px;
		border: 1px dashed var(--border);
		border-radius: 8px;
	}
</style>
