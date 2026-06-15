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
export { default as ReadinessPanel } from './overview/ReadinessPanel.svelte';
export { default as EngineSettingsCard } from './overview/EngineSettingsCard.svelte';
export { default as SegmentsDelivery } from './overview/SegmentsDelivery.svelte';
export {
	readinessRows,
	type ReadinessRow,
	type ReadinessState,
	type ReadinessInput,
} from './overview/readinessRows';
export { segmentRows, type SegmentRow } from './overview/segmentRows';
