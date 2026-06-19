export { default as ServiceTile } from './ServiceTile.svelte';
export type { ServiceTileSize } from './ServiceTile.svelte';
export { default as PageShell } from './PageShell.svelte';
export { mode, setMode, type RouterMode } from './modeStore';
export { default as RuleCard } from './RuleCard.svelte';
export { default as MatcherChip } from './MatcherChip.svelte';
export { default as OutboundTile } from './OutboundTile.svelte';
export type { OutboundTileSize } from './OutboundTile.svelte';
export { default as RulesPanel } from './RulesPanel.svelte';
export type {
  RuleAction,
  MatcherKind,
  MatcherChip as MatcherChipData,
  OutboundKind,
  OutboundDisplay,
  RuleCardData,
} from './types';

// F3 — StatusDrawer
export { default as StatusDrawer } from './StatusDrawer.svelte';
export { drawerOpen, openDrawer, closeDrawer, toggleDrawer } from './drawerStore';
export { default as DepRow } from './DepRow.svelte';
export { default as IssueRow } from './IssueRow.svelte';
export type { DepTone, DepEntry, IssueTone, IssueEntry } from './drawerData';
export { deriveDeps, deriveIssues } from './drawerData';

// F4a — FlowGraph hero
export { default as FlowGraph } from './FlowGraph.svelte';
export { default as SourceDrawer } from './SourceDrawer.svelte';
export { sourceDrawerOpen, openSourceDrawer, closeSourceDrawer } from './sourceDrawerStore';

// F4b — Trace Screen
export { default as TracePanel } from './TracePanel.svelte';
export { default as TracePathStation } from './TracePathStation.svelte';
export type { TracePathTone } from './TracePathStation.svelte';
export { default as TraceRuleRow } from './TraceRuleRow.svelte';
export {
  traceOpen,
  traceInput,
  traceResult,
  traceLoading,
  traceError,
  openTrace,
  closeTrace,
  runTrace,
  type TraceInput,
} from './traceStore';

// F5a — Templates Modal
export { default as TemplatesModal } from './TemplatesModal.svelte';
export { default as SbRouterServiceCatalogModal } from './SbRouterServiceCatalogModal.svelte';
export { default as SbRouterRuleSetCatalogModal } from './SbRouterRuleSetCatalogModal.svelte';
export {
  applyCatalogPresetsAsRuleSets,
  fullyAddedPresetNames,
  type ApplyRuleSetsFromCatalogResult,
} from './rulesetCatalogActions';
export { default as TemplatesFilterChip } from './TemplatesFilterChip.svelte';
export { default as TemplatesGroup } from './TemplatesGroup.svelte';
export { default as TemplateServiceTile } from './TemplateServiceTile.svelte';
export { default as TemplateRsRow } from './TemplateRsRow.svelte';
export { default as TemplatesFooter } from './TemplatesFooter.svelte';
export {
  templatesOpen, templatesSelection, templatesFilter, templatesQuery, templatesOutbound,
  openTemplatesModal, closeTemplatesModal, toggleTemplate, clearSelection,
  setFilter, setQuery, setOutbound,
} from './templatesStore';
export {
  buildTemplateList, filterByCategory, countByCategory,
  type TemplateCategory, type FilterKey, type ServiceTemplate, type RulesetTemplate,
  type TemplateItem, type TemplateGroup,
} from './templatesData';
export { submitTemplates, type SubmitResult } from './templatesActions';

// F5b — Add Rule Wizard
export { default as AddWizardPanel } from './AddWizardPanel.svelte';
export { default as StepPill } from './StepPill.svelte';
export { default as WizardStep } from './WizardStep.svelte';
export { default as OutboundOption } from './OutboundOption.svelte';
export { default as SelectedTemplatesRow } from './SelectedTemplatesRow.svelte';
export { default as CustomMatcherForm } from './CustomMatcherForm.svelte';
export {
  addWizardOpen,
  wizardOutboundCategory,
  wizardTunnelTags,
  wizardCustom,
  wizardEditRuleIndex,
  wizardEditMode,
  wizardExistingInlineRuleSetTag,
  wizardWasInlineText,
  openAddWizard,
  openEditWizard,
  closeAddWizard,
  setOutboundCategory,
  setTunnelTags,
  toggleTunnelTag,
  updateCustomField,
  resetWizardState,
  type OutboundCategory,
  type CustomMatcherFields,
} from './addWizardStore';
export {
  resolveOutbound,
  submitWizard,
  submitWizardEdit,
  ValidationError,
  type SubmitWizardArgs,
  type SubmitWizardEditArgs,
} from './addWizardActions';
export {
  dismissTemplatesModal,
  catalogIdsFromTemplatesSelection,
  setServiceTemplateSelection,
  setTemplateSelection,
} from './templatesStore';

// F5c — EmptyState
export { default as EmptyState } from './EmptyState.svelte';
export { default as EmptyHero } from './EmptyHero.svelte';

// F6 — Expert view components
export { default as RuleSetsTable } from './RuleSetsTable.svelte';
export { default as RoutingTable } from './RoutingTable.svelte';
export { default as OutboundsCompact } from './OutboundsCompact.svelte';
export { default as DnsServersCompact } from './DnsServersCompact.svelte';
export { default as DeviceProxyCompact } from './DeviceProxyCompact.svelte';
export { default as SidePanel } from './SidePanel.svelte';
export { default as StatStrip } from './StatStrip.svelte';
export type { StatCellData } from './StatStrip.svelte';
export { default as ExpertPanel } from './ExpertPanel.svelte';
export {
  expertPanelCollapse,
  toggleExpertPanelSection,
  setExpertPanelSectionCollapsed,
  isExpertPanelSectionCollapsed,
  type ExpertPanelSection,
  type ExpertPanelCollapseState,
} from './expertPanelCollapseStore';

// F7 — Mobile
export { default as MobileBottomBar } from './MobileBottomBar.svelte';

// Engine FATAL indication
export { default as EngineFatalModal } from './EngineFatalModal.svelte';

// Settings Drawer
export { default as SingboxRouterRedesignPage } from './SingboxRouterRedesignPage.svelte';
export {
  mergeAndSaveSettings,
  BYPASS_PRESETS,
  type BypassPresetMeta,
} from './settingsActions';
