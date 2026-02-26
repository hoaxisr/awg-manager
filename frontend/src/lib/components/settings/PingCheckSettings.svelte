<script lang="ts">
	import { Toggle } from '$lib/components/ui';
	import type { Settings, SystemInfo } from '$lib/types';

	interface Props {
		settings: Settings;
		systemInfo: SystemInfo | null;
		saving: boolean;
		onToggle: () => void;
		onSaveDefaults: () => void;
	}

	let {
		settings = $bindable(),
		systemInfo,
		saving,
		onToggle,
		onSaveDefaults,
	}: Props = $props();

	let showPingCheckDefaults = $state(false);
</script>

<div class="setting-row">
	<div class="flex flex-col gap-1">
		<span class="font-medium">Мониторинг туннелей</span>
		<span class="setting-description">
			Периодическая проверка связи через туннели.
			{#if systemInfo?.isOS5}
				На OS 5.x при потере связи интерфейс OpkgTun будет отключён для переключения на другой маршрут.
			{:else}
				На OS 4.x только мониторинг и логирование.
			{/if}
		</span>
	</div>
	<Toggle checked={settings.pingCheck.enabled} onchange={() => onToggle()} disabled={saving} />
</div>

{#if settings.pingCheck.enabled}
	<div style="padding: 0 0 0.875rem 0;">
		<button
			class="collapse-trigger"
			class:open={showPingCheckDefaults}
			onclick={() => showPingCheckDefaults = !showPingCheckDefaults}
		>
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
				<polyline points="9 18 15 12 9 6"></polyline>
			</svg>
			Настройки по умолчанию
		</button>

		{#if showPingCheckDefaults}
			<div class="settings-panel mt-3">
				<div class="form-group">
					<label for="method">Метод проверки</label>
					<select
						id="method"
						bind:value={settings.pingCheck.defaults.method}
						disabled={saving}
					>
						<option value="http">HTTP 204 (connectivitycheck.gstatic.com)</option>
						<option value="icmp">ICMP Ping</option>
					</select>
				</div>

				{#if settings.pingCheck.defaults.method === 'icmp'}
					<div class="form-group">
						<label for="target">IP для ping</label>
						<input
							type="text"
							id="target"
							bind:value={settings.pingCheck.defaults.target}
							placeholder="8.8.8.8"
							disabled={saving}
						/>
					</div>
				{/if}

				<div class="form-row">
					<div class="form-group mb-0">
						<label for="interval">Интервал (сек)</label>
						<input
							type="number"
							id="interval"
							bind:value={settings.pingCheck.defaults.interval}
							min="10"
							max="300"
							disabled={saving}
						/>
						<p class="form-hint">Как часто проверять</p>
					</div>

					<div class="form-group mb-0">
						<label for="deadInterval">Интервал для dead (сек)</label>
						<input
							type="number"
							id="deadInterval"
							bind:value={settings.pingCheck.defaults.deadInterval}
							min="30"
							max="600"
							disabled={saving}
						/>
						<p class="form-hint">Интервал после потери связи</p>
					</div>

					<div class="form-group mb-0">
						<label for="failThreshold">Порог ошибок</label>
						<input
							type="number"
							id="failThreshold"
							bind:value={settings.pingCheck.defaults.failThreshold}
							min="1"
							max="10"
							disabled={saving}
						/>
						<p class="form-hint">Неудач подряд для dead</p>
					</div>
				</div>

				<button
					class="btn btn-primary mt-3"
					onclick={onSaveDefaults}
					disabled={saving}
				>
					{saving ? 'Сохранение...' : 'Сохранить настройки'}
				</button>
			</div>
		{/if}
	</div>
{/if}

<style>
	.form-row {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
		gap: 1rem;
	}
</style>
