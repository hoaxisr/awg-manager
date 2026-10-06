<script lang="ts">
	import { api } from '$lib/api/client';
	import { Toggle, Button, Dropdown } from '$lib/components/ui';
	import type { HydraRouteConfig } from '$lib/types';
	import { m } from '$lib/i18n';

	interface Props {
		/** Внутри pane-container HR Neo — без заголовка и без вложенных card. */
		embedded?: boolean;
	}

	let { embedded = true }: Props = $props();

	let cfg = $state<HydraRouteConfig | null>(null);
	let dirty = $state(false);
	let saving = $state(false);
	let err = $state('');
	let advancedOpen = $state(false);

	async function load() {
		err = '';
		try {
			cfg = await api.getHydraRouteConfig();
			dirty = false;
		} catch (e: unknown) {
			err = e instanceof Error ? e.message : String(e);
		}
	}

	async function save() {
		if (!cfg) return;
		saving = true;
		err = '';
		try {
			cfg = await api.updateHydraRouteConfig(cfg);
			dirty = false;
		} catch (e: unknown) {
			err = e instanceof Error ? e.message : String(e);
		} finally {
			saving = false;
		}
	}

	$effect(() => {
		void load();
	});

	function touch<K extends keyof HydraRouteConfig>(key: K, val: HydraRouteConfig[K]) {
		if (!cfg) return;
		cfg = { ...cfg, [key]: val };
		dirty = true;
	}
</script>

<div class="settings-layout settings-pane" class:embedded>
	{#if !embedded}
		<header class="pane-header">
			<h2>{m.hrneo_settings_title()}</h2>
			{#if dirty}
				<Button variant="primary" size="sm" onclick={save} loading={saving}>
					{m.common_save()}
				</Button>
			{/if}
		</header>
	{:else if dirty}
		<div class="save-bar">
			<Button variant="primary" size="sm" onclick={save} loading={saving}>
				{m.common_save()}
			</Button>
		</div>
	{/if}

	{#if err}<div class="error-banner">{err}</div>{/if}

	{#if !cfg}
		<div class="empty">{m.common_loading()}</div>
	{:else}
		<div class="settings-stack">
			<div class="settings-block">
				<div class="section-label">{m.hrneo_settings_behavior()}</div>
				<div class="block-body" class:card={!embedded}>
					<div class="setting-row setting-row-toggle">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Auto-start</span>
							<span class="setting-description">{m.hrneo_settings_desc_autostart()}</span>
						</div>
						<Toggle checked={cfg.autoStart} onchange={(v) => touch('autoStart', v)} />
					</div>
					<div class="setting-row setting-row-toggle">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Clear ipset</span>
							<span class="setting-description">{m.hrneo_settings_desc_clear_ipset()}</span>
						</div>
						<Toggle checked={cfg.clearIPSet} onchange={(v) => touch('clearIPSet', v)} />
					</div>
					<div class="setting-row setting-row-toggle">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Conntrack flush</span>
							<span class="setting-description">{m.hrneo_settings_desc_conntrack()}</span>
						</div>
						<Toggle
							checked={cfg.conntrackFlush}
							onchange={(v) => touch('conntrackFlush', v)}
						/>
					</div>
					<div class="setting-row setting-row-toggle">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Global routing</span>
							<span class="setting-description warn">{m.hrneo_settings_desc_policies_warn()}</span>
						</div>
						<Toggle
							checked={cfg.globalRouting}
							onchange={(v) => touch('globalRouting', v)}
						/>
					</div>
				</div>
			</div>

			<div class="settings-block">
				<div class="section-label">Ipset</div>
				<div class="block-body" class:card={!embedded}>
					<div class="setting-row setting-row-toggle">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Enable timeout</span>
							<span class="setting-description">{m.hrneo_settings_desc_timeout()}</span>
						</div>
						<Toggle
							checked={cfg.ipsetEnableTimeout}
							onchange={(v) => touch('ipsetEnableTimeout', v)}
						/>
					</div>
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Ipset timeout</span>
							<span class="setting-description">{m.hrneo_settings_desc_timeout_seconds()}</span>
						</div>
						<input
							class="form-input num"
							type="number"
							value={cfg.ipsetTimeout}
							onchange={(e) =>
								touch('ipsetTimeout', Number((e.target as HTMLInputElement).value))}
						/>
					</div>
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Ipset maxelem</span>
							<span class="setting-description">{m.hrneo_settings_desc_maxelem()}</span>
						</div>
						<input
							class="form-input num"
							type="number"
							min="1"
							value={cfg.ipsetMaxElem}
							onchange={(e) =>
								touch('ipsetMaxElem', Number((e.target as HTMLInputElement).value))}
						/>
					</div>
				</div>
			</div>

			<div class="settings-block">
				<div class="section-label">{m.hrneo_settings_logging()}</div>
				<div class="block-body" class:card={!embedded}>
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Log mode</span>
						</div>
						<div class="log-select">
							<Dropdown
								value={cfg.log}
								options={[
									{ value: 'off', label: 'off' },
									{ value: 'console', label: 'console' },
									{ value: 'file', label: 'file' },
								]}
								onchange={(v) => touch('log', v)}
								fullWidth
							/>
						</div>
					</div>
					{#if cfg.log === 'file'}
						<div class="setting-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">Log file</span>
							</div>
							<input
								class="form-input"
								type="text"
								value={cfg.logFile}
								onchange={(e) =>
									touch('logFile', (e.target as HTMLInputElement).value)}
							/>
						</div>
					{/if}
				</div>
			</div>

			<div class="settings-block">
				<button type="button" class="disclosure" onclick={() => (advancedOpen = !advancedOpen)}>
					{advancedOpen ? '▾' : '▸'} {m.hrneo_settings_advanced()}
				</button>
				{#if advancedOpen}
					<div class="block-body" class:card={!embedded}>
						<div class="setting-row setting-row-toggle">
							<div class="flex flex-col gap-1">
								<span class="font-medium">DirectRoute enabled</span>
								<span class="setting-description">{m.hrneo_settings_desc_direct_route()}</span>
							</div>
							<Toggle
								checked={cfg.directRouteEnabled}
								onchange={(v) => touch('directRouteEnabled', v)}
							/>
						</div>
					</div>
				{/if}
			</div>
		</div>
	{/if}
</div>

<style>
	.settings-pane {
		min-width: 0;
	}

	.settings-pane.embedded {
		gap: var(--settings-gap);
	}

	.save-bar {
		display: flex;
		justify-content: flex-end;
	}

	.settings-pane.embedded .block-body {
		padding: 0.875rem 1rem;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		background: var(--color-bg-tertiary);
	}

	.log-select {
		min-width: 130px;
	}

	.pane-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding-bottom: 10px;
		border-bottom: 1px solid var(--border);
	}
	.pane-header h2 {
		margin: 0;
		font-size: 1.0625rem;
		color: var(--text-primary);
	}

	.error-banner {
		background: rgba(247, 118, 142, 0.1);
		border-left: 3px solid var(--error);
		color: var(--error);
		padding: 8px 12px;
		border-radius: 4px;
		font-size: 0.8125rem;
	}

	.empty {
		color: var(--text-muted);
		font-style: italic;
		padding: 14px;
	}

	.setting-description.warn {
		color: var(--warning);
	}

	.num {
		width: 140px;
		max-width: 276px;
		height: 32px;
		min-height: 32px;
		box-sizing: border-box;
		padding: 0.375rem 0.5rem;
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		color: var(--text-primary);
		font: inherit;
		font-size: 0.8125rem;
	}

	.form-input:not(.num) {
		width: 100%;
		max-width: 276px;
		height: 32px;
		min-height: 32px;
		box-sizing: border-box;
		padding: 0.375rem 0.5rem;
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		color: var(--text-primary);
		font: inherit;
		font-size: 0.8125rem;
	}

	.disclosure {
		background: transparent;
		border: none;
		color: var(--accent);
		font-size: 0.8125rem;
		font-weight: 500;
		cursor: pointer;
		padding: 0;
		margin-bottom: 0.5rem;
		font-family: inherit;
		text-align: left;
	}

	@media (max-width: 640px) {
		.setting-row-toggle {
			display: grid;
			grid-template-columns: minmax(0, 1fr) auto;
			align-items: center;
			gap: 0.75rem;
		}

		.setting-row-toggle > :first-child {
			min-width: 0;
		}

		.setting-row-toggle > :last-child {
			justify-self: end;
		}

		.num,
		.log-select {
			width: 100%;
			min-width: 0;
			max-width: none;
		}

		.form-input:not(.num) {
			max-width: none;
		}
	}
</style>
