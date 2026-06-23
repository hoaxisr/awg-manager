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
export { default as EngineSettingsCard } from './overview/EngineSettingsCard.svelte';
export { default as OverviewTab } from './overview/OverviewTab.svelte';
export { default as InboundsTab } from './inbounds/InboundsTab.svelte';
export { default as OutboundsTab } from './outbounds/OutboundsTab.svelte';
export { default as DnsTab } from './dns/DnsTab.svelte';
export { default as RuleSetsTab } from './rulesets/RuleSetsTab.svelte';
export { default as RoutesTab } from './routes/RoutesTab.svelte';
export { default as DevicesTab } from './devices/DevicesTab.svelte';
export {
	resolveDeviceTargeting,
	findRuleIndexForDevice,
	type DeviceMode,
	type DeviceTargeting,
} from './devices/deviceTargeting';
export {
	computeRuleSetUsageRefs,
	type RuleSetUsageRef,
} from './rulesets/ruleSetUsageRefs';
export {
	partitionOutbounds,
	type PartitionedOutbounds,
} from './outbounds/partitionOutbounds';
export {
	delayHealth,
	formatDelay,
	type DelayHealth,
} from './outbounds/formatDelay';
export {
	FakeIPPageShell,
	FakeIPHero,
	formatCompactCount,
	type ShellChip,
} from './shell';
export {
	activeCompositeRows,
	type ActiveCompositeRow,
	type ActiveCompositesInput,
} from './overview/activeComposites';
export {
	aggregateTotals,
	computeRate,
	type TrafficTotals,
	type TrafficRate,
	type RateSnapshot,
} from './overview/liveTraffic';
