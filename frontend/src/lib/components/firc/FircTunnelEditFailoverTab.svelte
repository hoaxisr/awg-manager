<script lang="ts">
	import { m } from '$lib/i18n';
	import type { FircTunnel } from '$lib/types/firc';
	import { Toggle } from '$lib/components/ui';
	import { FINGERPRINTS } from './fircTunnelEdit';

	interface Props {
		draft: FircTunnel;
	}

	let { draft = $bindable() }: Props = $props();
</script>

<div class="subtab-panel">
	<div class="settings-grid-2">
		<div class="sub-box">
			<div class="sub-box-title">{m.firc_tunnel_edit_box_failover()}</div>
			<div class="field-grid-2">
				<div class="field">
					<label class="lbl" for="firc-ed-act">{m.firc_tunnel_edit_active_label()}</label>
					<input
						id="firc-ed-act"
						class="inp mono"
						type="number"
						min="1"
						max="8"
						bind:value={draft.active}
					/>
				</div>
				<div class="field">
					<label class="lbl" for="firc-ed-by">{m.firc_tunnel_edit_by_label()}</label>
					<select id="firc-ed-by" class="inp" bind:value={draft.by}>
						<option value="connection">{m.firc_tunnel_edit_by_connection()}</option>
						<option value="site">{m.firc_tunnel_edit_by_site()}</option>
						<option value="site-client">{m.firc_tunnel_edit_by_site_client()}</option>
					</select>
				</div>
			</div>
			<div class="field-grid-2">
				<div class="field">
					<label class="lbl" for="firc-ed-int">{m.firc_tunnel_edit_interval_label()}</label>
					<input
						id="firc-ed-int"
						class="inp mono"
						type="number"
						min="5"
						max="300"
						bind:value={draft.interval}
					/>
				</div>
				<div class="field">
					<label class="lbl" for="firc-ed-sil">{m.firc_tunnel_edit_silence_label()}</label>
					<input
						id="firc-ed-sil"
						class="inp mono"
						type="number"
						min="5"
						max="300"
						bind:value={draft.silence}
					/>
				</div>
			</div>
		</div>

		<div class="sub-box">
			<div class="sub-box-title">{m.firc_tunnel_edit_box_network()}</div>
			<div class="field-grid-2">
				<div class="field">
					<label class="lbl" for="firc-ed-fp">{m.firc_tunnel_edit_fp_label()}</label>
					<select
						id="firc-ed-fp"
						class="inp"
						value={draft.advanced?.fingerprint ?? ''}
						onchange={(e) => {
							draft.advanced = {
								...draft.advanced,
								fingerprint: (e.currentTarget as HTMLSelectElement).value as
									| ''
									| 'chrome'
									| 'firefox',
							};
						}}
					>
						{#each FINGERPRINTS as fp}
							<option value={fp.value}>{fp.label()}</option>
						{/each}
					</select>
				</div>
				<div class="field">
					<label class="lbl" for="firc-ed-to">{m.firc_tunnel_edit_timeout_label()}</label>
					<input
						id="firc-ed-to"
						class="inp mono"
						type="number"
						min="2"
						max="60"
						value={draft.advanced?.timeout ?? 8}
						oninput={(e) => {
							draft.advanced = {
								...draft.advanced,
								timeout: Number((e.currentTarget as HTMLInputElement).value),
							};
						}}
					/>
				</div>
			</div>
			<div class="field-row">
				<div class="field-row-text">
					<span class="lbl">{m.firc_tunnel_edit_kernel_tcp_label()}</span>
					<span class="hint">{m.firc_tunnel_edit_kernel_tcp_hint()}</span>
				</div>
				<Toggle
					size="sm"
					spinner="none"
					checked={Boolean(draft.advanced?.kernelTcp)}
					onchange={(v) => {
						draft.advanced = { ...draft.advanced, kernelTcp: v };
					}}
				/>
			</div>
			<div class="field-row">
				<div class="field-row-text">
					<span class="lbl">{m.firc_tunnel_edit_insecure_label()}</span>
					<span class="hint">{m.firc_tunnel_edit_insecure_hint()}</span>
				</div>
				<Toggle
					size="sm"
					spinner="none"
					checked={Boolean(draft.advanced?.insecure)}
					onchange={(v) => {
						draft.advanced = { ...draft.advanced, insecure: v };
					}}
				/>
			</div>
		</div>
	</div>
</div>

<style>
	.subtab-panel {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.settings-grid-2 {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 10px;
	}
	@media (max-width: 680px) {
		.settings-grid-2 {
			grid-template-columns: 1fr;
		}
	}
	.sub-box {
		padding: 12px;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--bg-tertiary);
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.sub-box-title {
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
	}
	.field-grid-2 {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 8px;
	}
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.field-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
	}
	.field-row-text {
		display: flex;
		flex-direction: column;
		gap: 2px;
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
</style>
