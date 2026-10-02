<script lang="ts">
	import { m } from '$lib/i18n';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import { Button } from '$lib/components/ui';
	import { summonPukhosos } from '$lib/stores/pukhososSummon';
	import { PUKHOSOS_PATROL_MS } from '$lib/utils/pukhososPatrol';
	import { FlaskConical } from 'lucide-svelte';

	let summoning = $state(false);

	function handleSummon() {
		if (summoning) return;
		summoning = true;
		summonPukhosos();
		window.setTimeout(() => {
			summoning = false;
		}, PUKHOSOS_PATROL_MS);
	}
</script>

<div class="settings-block" id="experimental-settings">
	<div class="card">
		<SettingsSectionLabel label={m.settings_experimental_title()} icon={FlaskConical} tone="info" header cycleInVivid />
		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">{m.settings_experimental_vacuum_label()}</span>
				<span class="setting-description">
					{m.settings_experimental_vacuum_description()}
				</span>
			</div>
			<Button variant="secondary" size="md" onclick={handleSummon} disabled={summoning}>
				{summoning ? m.settings_experimental_vacuum_busy() : m.settings_experimental_vacuum_summon()}
			</Button>
		</div>
	</div>
</div>
