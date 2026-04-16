<script lang="ts">
	import { api } from '$lib/api/client';
	import { Toggle } from '$lib/components/ui';
	import type { HydraRouteConfig } from '$lib/types';

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

<div class="settings-pane">
	<header class="pane-header">
		<h2>Настройки демона hrneo</h2>
		{#if dirty}
			<button class="btn btn-primary btn-sm" onclick={save} disabled={saving}>
				{saving ? 'Сохранение…' : 'Сохранить'}
			</button>
		{/if}
	</header>

	{#if err}<div class="error-banner">{err}</div>{/if}

	{#if !cfg}
		<div class="empty">Загрузка…</div>
	{:else}
		<div class="settings-stack">
			<div>
				<div class="section-label">Поведение</div>
				<div class="settings-panel">
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Auto-start</span>
							<span class="setting-description">запуск при загрузке роутера</span>
						</div>
						<Toggle checked={cfg.autoStart} onchange={(v) => touch('autoStart', v)} />
					</div>
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Clear ipset</span>
							<span class="setting-description">очищать ipset при старте</span>
						</div>
						<Toggle checked={cfg.clearIPSet} onchange={(v) => touch('clearIPSet', v)} />
					</div>
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Conntrack flush</span>
							<span class="setting-description">сбрасывать conntrack при появлении нового IP</span>
						</div>
						<Toggle
							checked={cfg.conntrackFlush}
							onchange={(v) => touch('conntrackFlush', v)}
						/>
					</div>
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Global routing</span>
							<span class="setting-description warn">перезаписывает политики роутера — используйте осторожно</span>
						</div>
						<Toggle
							checked={cfg.globalRouting}
							onchange={(v) => touch('globalRouting', v)}
						/>
					</div>
				</div>
			</div>

			<div>
				<div class="section-label">Ipset</div>
				<div class="settings-panel">
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Enable timeout</span>
							<span class="setting-description">записи в ipset будут удаляться по таймауту</span>
						</div>
						<Toggle
							checked={cfg.ipsetEnableTimeout}
							onchange={(v) => touch('ipsetEnableTimeout', v)}
						/>
					</div>
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Ipset timeout</span>
							<span class="setting-description">секунды (21600 = 6 часов)</span>
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
							<span class="setting-description">макс. записей (0 → 65536)</span>
						</div>
						<input
							class="form-input num"
							type="number"
							value={cfg.ipsetMaxElem}
							onchange={(e) =>
								touch('ipsetMaxElem', Number((e.target as HTMLInputElement).value))}
						/>
					</div>
				</div>
			</div>

			<div>
				<div class="section-label">Логирование</div>
				<div class="settings-panel">
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Log mode</span>
						</div>
						<select
							class="form-select"
							value={cfg.log}
							onchange={(e) =>
								touch('log', (e.target as HTMLSelectElement).value)}
						>
							<option value="off">off</option>
							<option value="console">console</option>
							<option value="file">file</option>
						</select>
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

			<div>
				<button class="disclosure" onclick={() => (advancedOpen = !advancedOpen)}>
					{advancedOpen ? '▾' : '▸'} Расширенные (требуют перезапуск hrneo)
				</button>
				{#if advancedOpen}
					<div class="settings-panel">
						<div class="setting-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">DirectRoute enabled</span>
								<span class="setting-description">прямая маршрутизация на интерфейс</span>
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
		display: flex;
		flex-direction: column;
		gap: 14px;
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
	}

	.disclosure {
		background: transparent;
		border: none;
		color: var(--accent);
		font-size: 0.8125rem;
		font-weight: 500;
		cursor: pointer;
		padding: 4px 0;
		font-family: inherit;
	}
</style>
