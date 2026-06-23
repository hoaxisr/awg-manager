<!--
  Page-level host for the unified routing-mode switch. Mounted UNCONDITIONALLY on
  /routing (outside the {#if activeTab} chain) so the confirm + progress modals
  survive tab navigation while a switch is in flight. Both tab toggles drive it
  via the `modeSwitch` store; live progress comes from `fakeipTransition`.
-->
<script lang="ts">
	import ConfirmSwitch from '$lib/components/fakeip/ConfirmSwitch.svelte';
	import SwitchProgress from '$lib/components/fakeip/SwitchProgress.svelte';
	import { modeSwitch } from '$lib/stores/modeSwitch';
	import { fakeipTransition } from '$lib/stores/fakeipTransition';
</script>

<ConfirmSwitch
	open={$modeSwitch.phase === 'confirming'}
	from={$modeSwitch.from}
	to={$modeSwitch.target}
	busy={false}
	onConfirm={() => modeSwitch.confirm()}
	onCancel={() => modeSwitch.cancel()}
/>

<SwitchProgress
	open={$modeSwitch.phase === 'running'}
	state={$fakeipTransition}
	onClose={() => modeSwitch.closeProgress()}
/>
