import { writable, type Readable, type Writable } from 'svelte/store';
import { closeTrace } from './traceStore';
import { clearSelection } from './templatesStore';
import type { WizardEditMode } from './ruleWizardPrefill';

export type OutboundCategory = 'tunnel' | 'direct' | 'block';

export interface CustomMatcherFields {
  rulesList: string;
}

function emptyCustom(): CustomMatcherFields {
  return { rulesList: '' };
}

const openW: Writable<boolean> = writable(false);
const categoryW: Writable<OutboundCategory | null> = writable(null);
const tunnelW: Writable<string[]> = writable([]);
const customW: Writable<CustomMatcherFields> = writable(emptyCustom());
const editRuleIndexW: Writable<number | null> = writable(null);
const editModeW: Writable<WizardEditMode | null> = writable(null);
const existingInlineRuleSetTagW: Writable<string | null> = writable(null);
const wasInlineTextW: Writable<boolean> = writable(false);

export const addWizardOpen: Readable<boolean> = { subscribe: openW.subscribe };
export const wizardOutboundCategory: Readable<OutboundCategory | null> = { subscribe: categoryW.subscribe };
export const wizardTunnelTags: Readable<string[]> = { subscribe: tunnelW.subscribe };
export const wizardCustom: Readable<CustomMatcherFields> = { subscribe: customW.subscribe };
export const wizardEditRuleIndex: Readable<number | null> = { subscribe: editRuleIndexW.subscribe };
export const wizardEditMode: Readable<WizardEditMode | null> = { subscribe: editModeW.subscribe };
export const wizardExistingInlineRuleSetTag: Readable<string | null> = {
  subscribe: existingInlineRuleSetTagW.subscribe,
};
export const wizardWasInlineText: Readable<boolean> = { subscribe: wasInlineTextW.subscribe };

function setUrl(open: boolean) {
  if (typeof window === 'undefined') return;
  const url = new URL(window.location.href);
  if (open) {
    url.searchParams.set('add', '1');
    url.searchParams.delete('trace');
    url.searchParams.delete('q');
  } else {
    url.searchParams.delete('add');
    url.searchParams.delete('edit');
  }
  window.history.replaceState({}, '', url);
}

function clearEditState(): void {
  editRuleIndexW.set(null);
  editModeW.set(null);
  existingInlineRuleSetTagW.set(null);
  wasInlineTextW.set(false);
}

export function openAddWizard(): void {
  closeTrace();
  clearEditState();
  openW.set(true);
  setUrl(true);
}

export function openEditWizard(
  ruleIndex: number,
  prefill: {
    editMode: WizardEditMode;
    rulesList: string;
    outboundCategory: OutboundCategory;
    tunnelTags: string[];
    existingInlineRuleSetTag?: string;
    wasInlineText?: boolean;
  },
): void {
  closeTrace();
  editRuleIndexW.set(ruleIndex);
  editModeW.set(prefill.editMode);
  existingInlineRuleSetTagW.set(prefill.existingInlineRuleSetTag ?? null);
  wasInlineTextW.set(prefill.wasInlineText ?? false);
  categoryW.set(prefill.outboundCategory);
  tunnelW.set([...prefill.tunnelTags]);
  customW.set({ rulesList: prefill.rulesList });
  openW.set(true);
  setUrl(true);
}

export function closeAddWizard(): void {
  openW.set(false);
  categoryW.set(null);
  tunnelW.set([]);
  customW.set(emptyCustom());
  clearEditState();
  // Иначе selection из edit-prefill утекает в следующий «+ Правило».
  clearSelection();
  setUrl(false);
}

export function setOutboundCategory(c: OutboundCategory | null): void {
  categoryW.set(c);
}

export function setTunnelTags(tags: string[]): void {
  tunnelW.set([...tags]);
}

export function toggleTunnelTag(tag: string): void {
  tunnelW.update((tags) => {
    const i = tags.indexOf(tag);
    if (i >= 0) return tags.filter((t) => t !== tag);
    return [...tags, tag];
  });
}

export function updateCustomField<K extends keyof CustomMatcherFields>(
  key: K,
  value: CustomMatcherFields[K],
): void {
  customW.update((c) => ({ ...c, [key]: value }));
}

export function resetWizardState(): void {
  categoryW.set(null);
  tunnelW.set([]);
  customW.set(emptyCustom());
}

