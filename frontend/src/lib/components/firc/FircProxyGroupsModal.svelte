<script lang="ts">
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Modal } from '$lib/components/ui';
	import { ArrowDown, ArrowUp, Plus, RefreshCw, Trash2, X, Zap } from 'lucide-svelte';
	import {
		formatProbeLatency,
		isSelectableFircFallbackInterface,
		probeLatencyTone,
	} from '$lib/utils/fircFallback';
	import {
		MAX_GROUP_IFACES,
		formatIfaceOption,
		getIfaceProbe,
		ifaceLabel,
		normalizeGroupCheck,
		proxyGroupRuntime,
		randomHexId,
		resolveIfaceState,
		type FircGroupsRuntime,
		type FircProbeOptions,
	} from '$lib/utils/fircGroups';
	import type { FircGroupCheck, FircProxyGroup } from '$lib/types/firc';

	interface Props {
		open: boolean;
		draft: FircProxyGroup[];
		rt: FircGroupsRuntime;
		probingIfaces: Record<string, boolean>;
		probingGroups: Record<string, boolean>;
		saving?: boolean;
		/** true — кнопка «Применить»: черновик уходит вызывающему, на роутер его пишет
		 *  отдельное «Сохранить» (панель настроек). false — onsave сам сохраняет. */
		deferredSave?: boolean;
		onprobe: (ids: string[], opts?: FircProbeOptions) => void;
		onclose: () => void;
		onsave: () => void;
	}

	let {
		open,
		draft = $bindable(),
		rt,
		probingIfaces,
		probingGroups,
		saving = false,
		deferredSave = false,
		onprobe,
		onclose,
		onsave,
	}: Props = $props();

	function addDraftProxyGroup() {
		draft = [
			...draft,
			{
				id: randomHexId(),
				name: `Fallback #${draft.length + 1}`,
				interfaces: [],
				check: { mode: 'url', url: '' },
			},
		];
	}

	function removeDraftProxyGroup(index: number) {
		draft = draft.filter((_, i) => i !== index);
	}

	function toggleIfaceInDraftProxyGroup(pgIndex: number, ifaceId: string) {
		const pg = draft[pgIndex];
		if (!pg) return;
		const exists = pg.interfaces.includes(ifaceId);
		if (exists) {
			pg.interfaces = pg.interfaces.filter((x) => x !== ifaceId);
		} else {
			if (pg.interfaces.length >= MAX_GROUP_IFACES) {
				notifications.error(m.firc_groups_pg_max_ifaces({ max: MAX_GROUP_IFACES }));
				return;
			}
			pg.interfaces = [...pg.interfaces, ifaceId];
		}
	}

	function setDraftProxyGroupCheckMode(pgIndex: number, mode: FircGroupCheck['mode']) {
		const pg = draft[pgIndex];
		if (!pg) return;
		pg.check = normalizeGroupCheck({ ...pg.check, mode });
	}

	function setDraftProxyGroupCheckUrl(pgIndex: number, url: string) {
		const pg = draft[pgIndex];
		if (!pg?.check) return;
		pg.check = { ...pg.check, url };
	}

	function moveIfaceInDraftProxyGroup(pgIndex: number, iIdx: number, delta: -1 | 1) {
		const pg = draft[pgIndex];
		if (!pg) return;
		const target = iIdx + delta;
		if (target < 0 || target >= pg.interfaces.length) return;
		const copy = [...pg.interfaces];
		const [item] = copy.splice(iIdx, 1);
		copy.splice(target, 0, item);
		pg.interfaces = copy;
	}
</script>

<!-- Modal 4: Fallback Proxy Groups -->
<Modal
	{open}
	{onclose}
	title={m.firc_fallback_panel_title()}
	size="lg"
>
	<div class="modal-form">
		<p class="hint">
			{m.firc_groups_pg_hint_before()} <strong>Direct WAN</strong>{deferredSave
				? m.firc_settings_pg_hint_after()
				: m.firc_groups_pg_hint_after()}
		</p>

		{#each draft as pg, pgIdx (pg.id)}
			{@const draftRt = proxyGroupRuntime(rt, pg)}
			{@const isModalTesting = Boolean(probingGroups[`modal:${pg.id}`])}
			<div class="sec-box pg-editor-card">
				<div class="pg-card-head">
					<input
						type="text"
						class="inp"
						placeholder={m.firc_groups_pg_name_placeholder()}
						bind:value={pg.name}
					/>
					<Button
						variant="secondary"
						size="sm"
						disabled={pg.interfaces.length === 0 || isModalTesting}
						title={m.firc_fallback_test_group_title({ name: pg.name })}
						onclick={() =>
							onprobe(pg.interfaces, {
								groupKey: `modal:${pg.id}`,
								url: pg.check?.mode === 'url' && pg.check.url ? pg.check.url : undefined,
							})}
					>
						{#snippet iconBefore()}<Zap size={12} class={isModalTesting ? 'spin' : ''} />{/snippet}
						{isModalTesting ? '...' : m.firc_fallback_test()}
					</Button>
					<button
						type="button"
						class="route-action-btn danger"
						title={m.firc_groups_pg_delete_tip()}
						onclick={() => removeDraftProxyGroup(pgIdx)}
					>
						<Trash2 size={14} />
					</button>
				</div>

				<div class="pg-selected-chain">
					<div class="sec-cap">{m.firc_groups_pg_chain_cap()}</div>
					{#if pg.interfaces.length === 0}
						<div class="hint">{m.firc_groups_pg_chain_empty({ max: MAX_GROUP_IFACES })}</div>
					{:else}
						<div class="pg-chain-items">
							{#each pg.interfaces as ifaceId, iIdx (ifaceId)}
								{@const mem = draftRt.members.find((x) => x.id === ifaceId)}
								{@const mProbe = mem?.probe ?? getIfaceProbe(rt, ifaceId)}
								{@const mTone = probeLatencyTone(mProbe)}
								{@const isAct = draftRt.activeIface === ifaceId}
								{@const checking = Boolean(probingIfaces[ifaceId])}
								<div class="pg-chain-pill" class:is-active={isAct}>
									<span class="pg-rank-num">#{iIdx + 1}</span>
									<span
										class="pg-mem-dot"
										class:active={isAct}
										class:alive={!isAct && mem?.state === 'alive'}
										class:dead={mem?.state === 'dead'}
										class:unknown={!isAct && (!mem || mem.state === 'unknown')}
									></span>
									<span>{ifaceLabel(rt, ifaceId)}</span>
									<span class="pg-pill-id font-mono">({ifaceId})</span>
									{#if isAct}
										<span class="pg-mem-act-tag">{m.firc_fallback_active_tag()}</span>
									{/if}
									<button
										type="button"
										class="pg-delay-btn {mTone}"
										disabled={checking}
										title={m.firc_fallback_test_iface_title({ name: ifaceId })}
										onclick={() => onprobe([ifaceId])}
									>
										<span class="pg-delay-val">{formatProbeLatency(mProbe)}</span>
										{#if checking}
											<RefreshCw size={9} class="spin" />
										{/if}
									</button>
									<!-- Родные кнопки, не IconButton: его 28×28 и иконка 16px
									     раздувают пилюлю высотой ~20px. Стиль — .pg-chain-pill button. -->
									<button
										type="button"
										disabled={iIdx === 0}
										title={m.firc_groups_pg_move_up()}
										aria-label={m.firc_groups_pg_move_up()}
										onclick={() => moveIfaceInDraftProxyGroup(pgIdx, iIdx, -1)}
									>
										<ArrowUp size={12} />
									</button>
									<button
										type="button"
										disabled={iIdx === pg.interfaces.length - 1}
										title={m.firc_groups_pg_move_down()}
										aria-label={m.firc_groups_pg_move_down()}
										onclick={() => moveIfaceInDraftProxyGroup(pgIdx, iIdx, 1)}
									>
										<ArrowDown size={12} />
									</button>
									<button
										type="button"
										title={m.firc_groups_pg_remove_iface()}
										aria-label={m.firc_groups_pg_remove_iface()}
										onclick={() => toggleIfaceInDraftProxyGroup(pgIdx, ifaceId)}
									>
										<X size={12} />
									</button>
								</div>
							{/each}
						</div>
					{/if}
				</div>

				<div class="pg-candidates">
					<div class="sec-cap">{m.firc_groups_pg_candidates_cap()}</div>
					<div class="pg-cand-grid">
						{#each rt.interfaces.filter(isSelectableFircFallbackInterface) as opt (opt.id)}
							{@const idxInPg = pg.interfaces.indexOf(opt.id)}
							{@const isWan = opt.role === 'wan'}
							{@const cHealth = resolveIfaceState(rt, undefined, opt.id)}
							{@const cProbe = getIfaceProbe(rt, opt.id)}
							{@const cTone = probeLatencyTone(cProbe)}
							<button
								type="button"
								class="pg-cand-btn"
								class:selected={idxInPg >= 0}
								class:wan-cand={isWan}
								onclick={() => toggleIfaceInDraftProxyGroup(pgIdx, opt.id)}
							>
								<span class="iface-dot {cHealth.state}"></span>
								{#if idxInPg >= 0}
									<span class="pg-cand-rank">#{idxInPg + 1}</span>
								{/if}
								<span>{isWan ? `Direct (${formatIfaceOption(opt)})` : formatIfaceOption(opt)}</span>
								{#if cProbe}
									<span class="pill-delay-tag {cTone}">{formatProbeLatency(cProbe)}</span>
								{/if}
							</button>
						{/each}
					</div>
				</div>

				{#if pg.interfaces.length >= 2}
					{@const pgMode = pg.check?.mode}
					<div class="pg-check-box">
						<div class="pg-check-grid">
							<div class="field">
								<label class="lbl" for={`firc-pg-check-mode-${pg.id}`}>
									{m.firc_group_check_label()}
								</label>
								<select
									id={`firc-pg-check-mode-${pg.id}`}
									class="inp"
									value={pgMode ?? ''}
									onchange={(e) =>
										setDraftProxyGroupCheckMode(
											pgIdx,
											(e.currentTarget as HTMLSelectElement).value as FircGroupCheck['mode'],
										)}
								>
									{#if !pgMode}
										<option value="">{m.firc_pg_check_mode_groups()}</option>
									{/if}
									<option value="url">{m.firc_group_check_mode_url()}</option>
									<option value="keendns">{m.firc_group_check_mode_keendns()}</option>
									<option value="none">{m.firc_group_check_mode_none()}</option>
								</select>
							</div>
							{#if pgMode === 'url'}
								<div class="field">
									<label class="lbl" for={`firc-pg-check-url-${pg.id}`}>
										{m.firc_group_check_url_label()}
									</label>
									<input
										id={`firc-pg-check-url-${pg.id}`}
										type="text"
										class="inp font-mono"
										placeholder={m.firc_group_check_url_placeholder()}
										value={pg.check?.url ?? ''}
										oninput={(e) =>
											setDraftProxyGroupCheckUrl(pgIdx, (e.currentTarget as HTMLInputElement).value)}
									/>
								</div>
							{/if}
						</div>
						<p class="hint">
							{#if pgMode === 'url'}
								{m.firc_group_check_url_hint()}
							{:else if pgMode === 'keendns'}
								{m.firc_pg_check_keendns_hint()}
							{:else if pgMode === 'none'}
								{m.firc_pg_check_none_hint()}
							{:else}
								{m.firc_pg_check_groups_hint()}
							{/if}
							{#if pgMode}
								{m.firc_pg_check_scope_hint()}
							{/if}
						</p>
					</div>
				{/if}
			</div>
		{/each}

		<div>
			<Button variant="secondary" size="sm" onclick={addDraftProxyGroup}>
				{#snippet iconBefore()}<Plus size={14} />{/snippet}
				{m.firc_groups_pg_add()}
			</Button>
		</div>
	</div>

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" size="sm" onclick={onclose}>
				{m.common_cancel()}
			</Button>
			<Button variant="primary" size="sm" loading={saving} onclick={onsave}>
				{deferredSave ? m.common_apply() : m.common_save()}
			</Button>
		</div>
	{/snippet}
</Modal>

<style>
	.pill-delay-tag {
		font-family: var(--font-mono);
		font-size: 10.5px;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
		flex-shrink: 0;
	}

	.pill-delay-tag.good {
		color: var(--latency-color-ok);
	}

	.pill-delay-tag.medium {
		color: var(--latency-color-slow);
	}

	.pill-delay-tag.bad {
		color: var(--latency-color-fail);
	}

	.pill-delay-tag.none {
		color: var(--text-muted);
	}

	.iface-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
		display: inline-block;
	}

	.iface-dot.alive {
		background: var(--color-success);
		box-shadow: 0 0 4px var(--color-success-border);
	}

	.iface-dot.dead {
		background: var(--color-error);
		box-shadow: 0 0 4px var(--color-error-border);
	}

	.iface-dot.unknown {
		background: var(--text-muted);
		opacity: 0.55;
	}

	.inp {
		padding: 5px 9px;
		border-radius: var(--radius-sm);
		background: var(--bg-primary);
		border: 1px solid var(--border);
		color: var(--text-primary);
		font-size: 12.5px;
		font-family: inherit;
	}

	.inp:focus {
		outline: none;
		border-color: var(--accent);
	}

	.modal-form {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.sec-box {
		padding: 10px 12px;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.sec-cap {
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
	}

	.hint {
		margin: 0;
		font-size: 11.5px;
		color: var(--text-muted);
		line-height: 1.4;
	}

	.modal-footer-btns {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
		width: 100%;
	}

	.pg-mem-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.pg-mem-dot.active {
		background: var(--color-success);
		box-shadow: 0 0 6px var(--color-success-border);
		animation: firc-active-pulse 1.5s ease-in-out infinite;
	}

	.pg-mem-dot.alive {
		background: var(--color-success);
		opacity: 0.55;
	}

	.pg-mem-dot.dead {
		background: var(--color-error);
	}

	.pg-mem-dot.unknown {
		background: var(--text-muted);
		opacity: 0.55;
	}

	.pg-mem-act-tag {
		font-size: 9.5px;
		font-weight: 700;
		padding: 0 4px;
		border-radius: 3px;
		background: var(--color-success-tint);
		color: var(--color-success);
		border: 1px solid var(--color-success-border);
	}

	.pg-delay-btn {
		appearance: none;
		display: inline-flex;
		align-items: center;
		gap: 3px;
		padding: 1px 5px;
		border-radius: 4px;
		border: 1px solid transparent;
		background: transparent;
		font-family: var(--font-mono);
		font-size: 10.5px;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		line-height: 1.2;
		cursor: pointer;
		flex-shrink: 0;
		transition: background var(--t-fast), border-color var(--t-fast);
	}

	.pg-delay-btn:hover:not(:disabled) {
		background: var(--bg-tertiary);
		border-color: var(--border);
	}

	.pg-delay-btn.good {
		color: var(--latency-color-ok);
	}

	.pg-delay-btn.medium {
		color: var(--latency-color-slow);
	}

	.pg-delay-btn.bad {
		color: var(--latency-color-fail);
	}

	.pg-delay-btn.none {
		color: var(--text-muted);
	}

	.pg-delay-val {
		white-space: nowrap;
	}

	.pg-chain-pill.is-active {
		border-color: var(--color-success-border);
		background: color-mix(in srgb, var(--color-success) 10%, var(--bg-tertiary));
	}

	.pg-pill-id {
		font-size: 10.5px;
		color: var(--text-muted);
	}

	.pg-editor-card {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.pg-card-head {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.pg-chain-items {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		margin-top: 6px;
	}

	.pg-chain-pill {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 4px 8px;
		border-radius: 6px;
		font-size: 12px;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
	}

	.pg-chain-pill button {
		border: none;
		background: transparent;
		color: var(--text-secondary);
		cursor: pointer;
		padding: 0 2px;
		font-size: 11px;
	}

	.pg-chain-pill button:not(.pg-delay-btn) {
		display: inline-flex;
		align-items: center;
	}

	.pg-chain-pill button:not(.pg-delay-btn):disabled {
		opacity: 0.4;
		cursor: default;
	}

	.pg-rank-num {
		font-weight: 700;
		color: var(--color-accent);
		font-size: 11px;
	}

	.pg-cand-grid {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		margin-top: 6px;
	}

	.pg-cand-btn {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 5px 10px;
		border-radius: 6px;
		font-size: 12px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		color: var(--text-secondary);
		cursor: pointer;
	}

	.pg-cand-btn.selected {
		background: color-mix(in srgb, var(--color-accent) 14%, var(--bg-secondary));
		border-color: color-mix(in srgb, var(--color-accent) 45%, var(--border));
		color: var(--text-primary);
		font-weight: 500;
	}

	.pg-cand-btn.wan-cand {
		border-style: dashed;
	}

	.pg-cand-rank {
		font-size: 10px;
		font-weight: 700;
		padding: 1px 5px;
		border-radius: 4px;
		background: color-mix(in srgb, var(--color-accent) 22%, transparent);
		color: var(--color-accent);
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

	.pg-check-box {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding-top: 8px;
		border-top: 1px dashed var(--border);
	}

	.pg-check-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
		gap: 8px;
	}
</style>
