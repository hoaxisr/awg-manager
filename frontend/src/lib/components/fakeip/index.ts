// Barrel for FakeIP page components (lib/components/fakeip/*).
// Populated incrementally by Slice 1E tasks (chip-shell, transition screens,
// overview) and the later chip-page slices.
export { default as NotEnabledScreen } from './NotEnabledScreen.svelte';
export { default as ConfirmSwitch } from './ConfirmSwitch.svelte';
export { default as SwitchProgress } from './SwitchProgress.svelte';
export { deriveFakeIPEngineState, type FakeIPEngineState } from './engineState';
export {
	humanLabel,
	switchConsequences,
	type RoutingMode,
} from './switchConsequences';
