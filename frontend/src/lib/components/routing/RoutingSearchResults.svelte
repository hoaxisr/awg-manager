<script lang="ts">
    interface MatchedRule {
        id: string;
        name: string;
        type: 'dns' | 'ip';
        matches: string[];
        totalMatches: number;
    }

    interface ResolveMatch {
        domain: string;
        ips: string[];
        rules: MatchedRule[];
    }

    interface Props {
        dnsResults: MatchedRule[];
        ipResults: MatchedRule[];
        resolveMatch: ResolveMatch | null;
        resolving: boolean;
        resolveError: string;
    }

    let { dnsResults, ipResults, resolveMatch, resolving, resolveError }: Props = $props();

    const MAX_SHOWN = 3;
</script>

<div class="search-results">
    {#if dnsResults.length > 0}
        <div class="results-group">
            <div class="results-group-title">DNS-правила</div>
            {#each dnsResults as rule}
                <div class="result-item">
                    <svg class="result-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
                        <circle cx="12" cy="12" r="10"/>
                        <line x1="2" y1="12" x2="22" y2="12"/>
                        <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>
                    </svg>
                    <span class="result-rule-name">{rule.name}</span>
                    <span class="result-matches">
                        {rule.matches.slice(0, MAX_SHOWN).join(', ')}
                        {#if rule.totalMatches > MAX_SHOWN}
                            <span class="result-more">+{rule.totalMatches - MAX_SHOWN} ещё</span>
                        {/if}
                    </span>
                </div>
            {/each}
        </div>
    {/if}

    {#if ipResults.length > 0}
        <div class="results-group">
            <div class="results-group-title">IP-правила</div>
            {#each ipResults as rule}
                <div class="result-item">
                    <svg class="result-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
                        <rect x="2" y="2" width="20" height="20" rx="2"/>
                        <path d="M7 8h10M7 12h10M7 16h6"/>
                    </svg>
                    <span class="result-rule-name">{rule.name}</span>
                    <span class="result-matches">
                        {rule.matches.slice(0, MAX_SHOWN).join(', ')}
                        {#if rule.totalMatches > MAX_SHOWN}
                            <span class="result-more">+{rule.totalMatches - MAX_SHOWN} ещё</span>
                        {/if}
                    </span>
                </div>
            {/each}
        </div>
    {/if}

    {#if resolving}
        <div class="results-group">
            <div class="results-group-title resolve-loading">
                <span class="spinner-sm"></span>
                Резолв домена...
            </div>
        </div>
    {/if}

    {#if resolveMatch}
        <div class="results-group">
            <div class="results-group-title">
                Резолв: {resolveMatch.domain} → {resolveMatch.ips.join(', ')}
            </div>
            {#if resolveMatch.rules.length > 0}
                {#each resolveMatch.rules as rule}
                    <div class="result-item">
                        {#if rule.type === 'dns'}
                            <svg class="result-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
                                <circle cx="12" cy="12" r="10"/>
                                <line x1="2" y1="12" x2="22" y2="12"/>
                                <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>
                            </svg>
                        {:else}
                            <svg class="result-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
                                <rect x="2" y="2" width="20" height="20" rx="2"/>
                                <path d="M7 8h10M7 12h10M7 16h6"/>
                            </svg>
                        {/if}
                        <span class="result-rule-name">{rule.name}</span>
                        <span class="result-matches">
                            попадает в {rule.matches.slice(0, MAX_SHOWN).join(', ')}
                            {#if rule.totalMatches > MAX_SHOWN}
                                <span class="result-more">+{rule.totalMatches - MAX_SHOWN} ещё</span>
                            {/if}
                        </span>
                    </div>
                {/each}
            {:else}
                <div class="result-item result-empty">Не попадает ни в одну подсеть</div>
            {/if}
        </div>
    {/if}

    {#if resolveError}
        <div class="results-group">
            <div class="result-item result-error">{resolveError}</div>
        </div>
    {/if}

    {#if dnsResults.length === 0 && ipResults.length === 0 && !resolving && !resolveMatch && !resolveError}
        <div class="result-item result-empty">Не найдено ни в одном правиле</div>
    {/if}
</div>

<style>
    .search-results {
        position: absolute;
        left: 0;
        right: 0;
        top: 100%;
        z-index: 50;
        background: var(--color-surface-100);
        border: 1px solid var(--color-surface-300);
        border-radius: 0 0 8px 8px;
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
        max-height: 400px;
        overflow-y: auto;
    }

    .results-group {
        padding: 4px 0;
        border-bottom: 1px solid var(--color-surface-200);
    }

    .results-group:last-child {
        border-bottom: none;
    }

    .results-group-title {
        padding: 6px 12px;
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-surface-500);
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }

    .resolve-loading {
        display: flex;
        align-items: center;
        gap: 8px;
    }

    .spinner-sm {
        width: 14px;
        height: 14px;
        border: 2px solid var(--color-surface-300);
        border-top-color: var(--color-primary-500);
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
    }

    @keyframes spin {
        to { transform: rotate(360deg); }
    }

    .result-item {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 8px 12px;
        font-size: 0.875rem;
    }

    .result-item:hover {
        background: var(--color-surface-200);
    }

    .result-icon {
        flex-shrink: 0;
        color: var(--color-surface-400);
    }

    .result-rule-name {
        font-weight: 500;
        white-space: nowrap;
        color: var(--color-surface-900);
    }

    .result-matches {
        color: var(--color-surface-500);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .result-more {
        color: var(--color-primary-500);
        font-weight: 500;
    }

    .result-empty {
        color: var(--color-surface-400);
        font-style: italic;
    }

    .result-error {
        color: var(--color-error-500);
    }
</style>
