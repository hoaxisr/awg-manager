<!-- Карточка «Zapret»: есть, только если он установлен; статус — у «Продвинутого». -->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import { runZapretControl } from '$lib/components/zapret';
	import { zapretStatusIfExpert } from '$lib/stores/zapret';
	import type { ZapretControlAction } from '$lib/types';
	import { Zap } from 'lucide-svelte';
	import ComponentRow from './ComponentRow.svelte';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';

	const status = $derived($zapretStatusIfExpert?.data ?? null);
	const running = $derived(status?.running ?? false);
	let busy = $state(false);

	async function control(action: ZapretControlAction) {
		busy = true;
		try {
			await runZapretControl(action, status?.base ?? '');
		} finally {
			busy = false;
		}
	}
</script>

{#if status?.installed}
	<div class="settings-block">
		<div class="card">
			<SettingsSectionLabel label="Zapret" icon={Zap} tone="amber" header />
			<ComponentRow
				label="Zapret"
				{running}
				ariaLabel={running ? m.settings_integrations_zapret_running_aria() : m.settings_integrations_zapret_stopped_aria()}
			>
				<span class="component-sub">{running ? m.zapret_state_running() : m.zapret_state_stopped()}</span>
				{#if status.lastError}
					<span class="setting-description warning" title={status.lastError}>{status.lastError}</span>
				{/if}
				{#snippet actions()}
					<Button variant="secondary" size="sm" href="/routing?tab=zapret">{m.settings_integrations_open()}</Button>
					{#if running}
						<Button variant="secondary" size="sm" onclick={() => control('restart')} loading={busy}>{m.common_restart()}</Button>
						<Button variant="danger" size="sm" onclick={() => control('stop')} loading={busy}>{m.common_stop()}</Button>
					{:else}
						<Button variant="success" size="sm" onclick={() => control('start')} loading={busy}>{m.common_start()}</Button>
					{/if}
				{/snippet}
			</ComponentRow>
		</div>
	</div>
{/if}
