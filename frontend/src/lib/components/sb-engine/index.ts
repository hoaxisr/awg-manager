// Компоненты страницы «Движок» (nav-v3, волна 5D2a).
export { default as EngineHeader } from './EngineHeader.svelte';
export { default as EngineStatStrip } from './EngineStatStrip.svelte';
export { default as EngineCaptureCard } from './EngineCaptureCard.svelte';
export { default as EngineSelectiveCard } from './EngineSelectiveCard.svelte';
export { default as EngineExclusionsCard } from './EngineExclusionsCard.svelte';
export { CAPTURE_MODES, captureModeCopy, captureModeOptions, type CaptureModeCopy } from './captureModes';
export { applyEngineSettings } from './engineSettings';
export {
	DNS_BYPASS_PRESET_IDS,
	isDnsBypassPreset,
	netfilterExclusionsScope,
	partitionBypassPresets,
	type ExclusionsScope,
} from './exclusionsScope';
export {
	canOpenEngineFatal,
	deriveEnginePill,
	isEngineRunning,
	normalizeRoutingMode,
	type EnginePill,
	type EnginePillTone,
	type EngineRoutingMode,
	type EngineRunState,
	type EngineStatusInput,
} from './engineRunState';
