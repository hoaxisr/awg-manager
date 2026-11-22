<script lang="ts">
	import { m } from '$lib/i18n';
	import type { TelegramUpstream } from '$lib/types';
	import { Button, Toggle } from '$lib/components/ui';
	import { Plus, Trash2 } from 'lucide-svelte';

	interface Props {
		upstreams: TelegramUpstream[];
		egressOptions: { value: string; label: string; available: boolean }[];
	}

	let { upstreams = $bindable([]), egressOptions }: Props = $props();

	let customDeviceIds = $state<Record<string, boolean>>({});

	function hasKnownEgressOption(dev?: string): boolean {
		const clean = (dev || '').trim();
		if (!clean || clean === 'direct') return true;
		return egressOptions.some((o) => o.value === clean);
	}

	function isCustomDeviceRow(u: TelegramUpstream): boolean {
		return Boolean(customDeviceIds[u.id]) || !hasKnownEgressOption(u.device);
	}

	function addUpstream() {
		upstreams = [
			...upstreams,
			{
				id: `up-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 5)}`,
				type: 'direct',
				device: 'direct',
				weight: 1,
				enabled: true,
			},
		];
	}

	function removeUpstream(idx: number) {
		if (upstreams.length <= 1) return;
		upstreams = upstreams.filter((_, i) => i !== idx);
	}

	function switchType(u: TelegramUpstream, nextType: 'direct' | 'socks5') {
		u.type = nextType;
		if (nextType === 'direct') {
			u.device = u.device || 'direct';
		} else {
			u.address = u.address || '127.0.0.1:1099';
		}
	}
</script>

<div class="upstreams-editor">
	<div class="upstreams-head">
		<div class="upstreams-head-text">
			<span class="upstreams-title">{m.tg_upstreams_title()}</span>
			<span class="upstreams-hint">{m.tg_upstreams_hint()}</span>
		</div>
		<Button variant="secondary" size="sm" onclick={addUpstream}>
			<Plus size={13} class="mr-1" />
			{m.tg_upstreams_add()}
		</Button>
	</div>

	<div class="upstreams-list">
		{#each upstreams as u, idx (u.id || idx)}
			<div class="upstream-row" class:disabled={!u.enabled}>
				<div class="upstream-top">
					<Toggle
						checked={u.enabled}
						onchange={(v) => {
							u.enabled = v;
						}}
					/>

					<select
						aria-label={m.tg_upstreams_title()}
						value={u.type}
						onchange={(e) =>
							switchType(
								u,
								(e.currentTarget as HTMLSelectElement).value === 'socks5'
									? 'socks5'
									: 'direct',
							)}
						class="select-input type-select"
					>
						<option value="direct">{m.tg_upstreams_type_direct()}</option>
						<option value="socks5">{m.tg_upstreams_type_socks5()}</option>
					</select>

					<label class="weight-wrap">
						<span class="weight-label">{m.tg_upstreams_weight_label()}:</span>
						<input
							type="number"
							min="1"
							max="65535"
							value={u.weight || 1}
							oninput={(e) => {
								const parsed = Number((e.currentTarget as HTMLInputElement).value);
								u.weight = Number.isFinite(parsed) && parsed >= 1 ? Math.min(65535, Math.floor(parsed)) : 1;
							}}
							class="text-input weight-input font-mono"
						/>
					</label>

					<button
						type="button"
						class="btn-row-remove"
						aria-label={m.tg_upstreams_remove_aria()}
						title={m.tg_upstreams_remove_aria()}
						disabled={upstreams.length <= 1}
						onclick={() => removeUpstream(idx)}
					>
						<Trash2 size={14} />
					</button>
				</div>

				{#if u.type === 'socks5'}
					<div class="upstream-fields-socks">
						<input
							type="text"
							aria-label={m.tg_upstreams_socks_addr_label()}
							bind:value={u.address}
							class="text-input font-mono addr-input"
							placeholder={m.tg_upstreams_socks_addr_placeholder()}
						/>
						<input
							type="text"
							bind:value={u.username}
							class="text-input cred-input"
							placeholder={m.tg_upstreams_socks_user_placeholder()}
						/>
						<input
							type="password"
							bind:value={u.password}
							class="text-input cred-input"
							autocomplete="new-password"
							placeholder={u.password_set
								? m.tg_upstreams_socks_pass_saved_placeholder()
								: m.tg_upstreams_socks_pass_placeholder()}
						/>
					</div>
				{:else}
					<div class="upstream-fields-direct">
						{#if !isCustomDeviceRow(u)}
							<select
								aria-label={m.tg_upstreams_iface_label()}
								value={u.device || 'direct'}
								onchange={(e) => {
									const val = (e.currentTarget as HTMLSelectElement).value;
									if (val === '__custom__') {
										customDeviceIds = { ...customDeviceIds, [u.id]: true };
										if (!u.device || u.device === 'direct') {
											u.device = '';
										}
									} else {
										u.device = val;
									}
								}}
								class="select-input iface-select"
							>
								{#each egressOptions as opt (opt.value)}
									<option value={opt.value}>{opt.label}</option>
								{/each}
								<option value="__custom__">{m.tg_upstreams_iface_custom()}</option>
							</select>
						{:else}
							<div class="iface-custom-row">
								<input
									type="text"
									aria-label={m.tg_upstreams_iface_label()}
									bind:value={u.device}
									class="text-input font-mono iface-select"
									placeholder="nwg1"
								/>
								<Button
									variant="secondary"
									size="sm"
									onclick={() => {
										customDeviceIds = { ...customDeviceIds, [u.id]: false };
										if (!hasKnownEgressOption(u.device)) {
											u.device = egressOptions[0]?.value || 'direct';
										}
									}}
								>
									{m.tg_upstreams_iface_list()}
								</Button>
							</div>
						{/if}
					</div>
				{/if}
			</div>
		{/each}
	</div>
</div>

<style>
	.upstreams-editor {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.upstreams-head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 10px;
		flex-wrap: wrap;
	}

	.upstreams-head-text {
		display: flex;
		flex-direction: column;
		gap: 2px;
		flex: 1;
		min-width: 180px;
	}

	.upstreams-title {
		font-size: 12px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.upstreams-hint {
		font-size: 11px;
		color: var(--color-text-muted);
		line-height: 1.35;
	}

	.upstreams-list {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.upstream-row {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 8px 10px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}

	.upstream-row.disabled {
		opacity: 0.65;
	}

	.upstream-top {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.type-select {
		flex: 1;
		min-width: 145px;
	}

	.weight-wrap {
		display: inline-flex;
		align-items: center;
		gap: 6px;
	}

	.weight-label {
		font-size: 11.5px;
		color: var(--color-text-muted);
	}

	.weight-input {
		width: 68px;
		text-align: right;
	}

	.btn-row-remove {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 28px;
		height: 28px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		cursor: pointer;
	}

	.btn-row-remove:hover:not(:disabled) {
		color: var(--color-error);
		border-color: var(--color-error);
		background: var(--color-error-tint);
	}

	.btn-row-remove:disabled {
		opacity: 0.4;
		cursor: not-allowed;
	}

	.upstream-fields-direct,
	.iface-custom-row {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.iface-select {
		flex: 1;
		min-width: 0;
	}

	.upstream-fields-socks {
		display: grid;
		grid-template-columns: 1.4fr 1fr 1fr;
		gap: 6px;
	}

	.text-input,
	.select-input {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 5px 8px;
		font-size: 12.5px;
		color: var(--color-text-primary);
		min-width: 0;
	}

	.font-mono {
		font-family: var(--font-mono);
	}

	@media (max-width: 600px) {
		.upstream-fields-socks {
			grid-template-columns: 1fr;
		}
	}
</style>
