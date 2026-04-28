import { writable } from 'svelte/store';
import type { LogEntry } from '$lib/types';

export interface ContextMenuState {
  open: boolean;
  x: number;
  y: number;
  log: LogEntry | null;
  onCopyLine?: () => void;
  onCopyMessage?: () => void;
  onFilterScope?: () => void;
  onFilterLevel?: () => void;
}

export const contextMenu = writable<ContextMenuState>({
  open: false,
  x: 0,
  y: 0,
  log: null,
});

export function openContextMenu(
  e: MouseEvent,
  log: LogEntry,
  handlers: Pick<ContextMenuState, 'onCopyLine' | 'onCopyMessage' | 'onFilterScope' | 'onFilterLevel'>,
) {
  e.preventDefault();
  contextMenu.set({
    open: true,
    x: e.clientX,
    y: e.clientY,
    log,
    onCopyLine: handlers.onCopyLine,
    onCopyMessage: handlers.onCopyMessage,
    onFilterScope: handlers.onFilterScope,
    onFilterLevel: handlers.onFilterLevel,
  });
}

export function closeContextMenu() {
  contextMenu.update((s) => ({ ...s, open: false, log: null }));
}
