<script lang="ts">
	import { SegmentedControl } from '$lib/components/ui';
	import { proxyPanelMode, setProxyPanelMode, type ProxyPanelMode } from './modeStore';

	const mode = proxyPanelMode;
</script>

<div class="proxy-mode-toggle">
	<SegmentedControl
		ariaLabel="Режим настройки"
		value={$mode}
		options={[
			{ value: 'simple', label: 'Мастер' },
			{ value: 'expert', label: 'Эксперт' }
		]}
		onchange={(v) => setProxyPanelMode(v as ProxyPanelMode)}
	/>
</div>

<style>
	.proxy-mode-toggle {
		display: flex;
		justify-content: flex-end;
	}
</style>
