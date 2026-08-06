// Компоненты страницы «Движок» (nav-v3, волна 5D2a).
export { default as EngineHeader } from './EngineHeader.svelte';
export { default as EngineStatStrip } from './EngineStatStrip.svelte';
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
