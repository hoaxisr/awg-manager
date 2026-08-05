<!--
  Хост общего переключателя режима маршрутизации. Смонтирован БЕЗУСЛОВНО в layout
  группы движка (routes/sb/(engine)/+layout.svelte) — вне гейта и вне ветвлений,
  — чтобы подтверждение и прогресс пережили уход со страницы, пока переключение
  идёт (оно занимает минуты и продолжается в фоне). Запросы на смену режима
  приходят из стора `modeSwitch` (FakeIPTab, StatusDrawer), живой прогресс — из
  `fakeipTransition`.
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
	transitionState={$fakeipTransition}
	onClose={() => modeSwitch.closeProgress()}
/>
