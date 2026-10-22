const PREFIX = 'ui.tabs.lastTab:';

/** Callers opt in with a stable, feature-specific key. */
export function tabMemoryKey(key: string): string {
    return `${PREFIX}${key}`;
}

export function readTabMemory(key: string, available: string[]): string | null {
    try {
        const saved = localStorage.getItem(key);
        return saved && available.includes(saved) ? saved : null;
    } catch {
        return null;
    }
}

export function writeTabMemory(key: string, id: string): void {
    try {
        localStorage.setItem(key, id);
    } catch {
        // Browser storage is optional, including in private mode.
    }
}
