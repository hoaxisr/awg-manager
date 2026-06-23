<script lang="ts">
    import { ServiceIcon } from '$lib/components/dnsroutes';
    import type { MatchedRule, ResolveMatch } from './types';
    import { X } from 'lucide-svelte';

    interface Props {
        dnsResults: MatchedRule[];
        ipResults: MatchedRule[];
        resolveMatch: ResolveMatch | null;
        resolving: boolean;
        resolveError: string;
        onRuleClick?: (id: string, type: 'dns' | 'ip') => void;
        onClose?: () => void;
    }

    let { dnsResults, ipResults, resolveMatch, resolving, resolveError, onRuleClick, onClose }: Props = $props();

    const MAX_SHOWN = 4;
</script>

<div class="search-results">
    {#if onClose}
        <button class="results-close" onclick={onClose} title="Закрыть (Esc)" aria-label="Закрыть результаты">
            <X size={14} aria-hidden="true" />
        </button>
    {/if}
    {#if dnsResults.length > 0}
        <div class="results-group">
            <div class="results-group-title">DNS-правила</div>
            {#each dnsResults as rule}
                <button class="result-card" onclick={() => onRuleClick?.(rule.id, rule.type)}>
                    <ServiceIcon name={rule.name} iconUrl={rule.iconUrl} size={32} />
                    <div class="result-body">
                        <div class="result-title">
                            <span class="result-led" class:led-on={rule.enabled} class:led-off={!rule.enabled}></span>
                            <span class="result-name">{rule.name}</span>
                        </div>
                        <div class="result-meta">
                            {#if rule.domainCount > 0}
                                <span>{rule.domainCount} доменов</span>
                            {/if}
                            {#if rule.sourceSummary}
                                <span>{rule.sourceSummary}</span>
                            {/if}
                            {#if rule.tunnelName}
                                <span class="result-tunnel">&rarr; <code>{rule.tunnelName}</code></span>
                            {/if}
                        </div>
                        <div class="result-match">
                            {rule.matches.slice(0, MAX_SHOWN).join(', ')}
                            {#if rule.totalMatches > MAX_SHOWN}
                                <span class="result-more">+{rule.totalMatches - MAX_SHOWN} ещё</span>
                            {/if}
                        </div>
                    </div>
                    <span class="result-arrow">&rsaquo;</span>
                </button>
            {/each}
        </div>
    {/if}

    {#if ipResults.length > 0}
        <div class="results-group">
            <div class="results-group-title">IP-правила</div>
            {#each ipResults as rule}
                <button class="result-card" onclick={() => onRuleClick?.(rule.id, rule.type)}>
                    <ServiceIcon name={rule.name} iconUrl={rule.iconUrl} size={32} />
                    <div class="result-body">
                        <div class="result-title">
                            <span class="result-led" class:led-on={rule.enabled} class:led-off={!rule.enabled}></span>
                            <span class="result-name">{rule.name}</span>
                        </div>
                        <div class="result-meta">
                            {#if rule.sourceSummary}
                                <span>{rule.sourceSummary}</span>
                            {/if}
                            {#if rule.tunnelName}
                                <span class="result-tunnel">&rarr; <code>{rule.tunnelName}</code></span>
                            {/if}
                        </div>
                        <div class="result-match">
                            {rule.matches.slice(0, MAX_SHOWN).join(', ')}
                            {#if rule.totalMatches > MAX_SHOWN}
                                <span class="result-more">+{rule.totalMatches - MAX_SHOWN} ещё</span>
                            {/if}
                        </div>
                    </div>
                    <span class="result-arrow">&rsaquo;</span>
                </button>
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
        <div class="results-group resolve-group">
            <div class="results-group-title">
                Резолв: {resolveMatch.domain} &rarr; <span class="resolve-ips">{resolveMatch.ips.join(', ')}</span>
            </div>
            {#if resolveMatch.rules.length > 0}
                {#each resolveMatch.rules as rule}
                    <button class="result-card" onclick={() => onRuleClick?.(rule.id, rule.type)}>
                        <ServiceIcon name={rule.name} iconUrl={rule.iconUrl} size={32} />
                        <div class="result-body">
                            <div class="result-title">
                                <span class="result-led" class:led-on={rule.enabled} class:led-off={!rule.enabled}></span>
                                <span class="result-name">{rule.name}</span>
                            </div>
                            <div class="result-meta">
                                {#if rule.tunnelName}
                                    <span class="result-tunnel">&rarr; <code>{rule.tunnelName}</code></span>
                                {/if}
                            </div>
                            <div class="result-match">
                                попадает в {rule.matches.slice(0, MAX_SHOWN).join(', ')}
                                {#if rule.totalMatches > MAX_SHOWN}
                                    <span class="result-more">+{rule.totalMatches - MAX_SHOWN} ещё</span>
                                {/if}
                            </div>
                        </div>
                        <span class="result-arrow">&rsaquo;</span>
                    </button>
                {/each}
            {:else}
                <div class="result-empty">Не попадает ни в одну подсеть</div>
            {/if}
        </div>
    {/if}

    {#if resolveError}
        <div class="results-group">
            <div class="result-error">{resolveError}</div>
        </div>
    {/if}

    {#if dnsResults.length === 0 && ipResults.length === 0 && !resolving && !resolveMatch && !resolveError}
        <div class="result-empty">Не найдено ни в одном правиле</div>
    {/if}
</div>

<style>
    .search-results {
        background: var(--bg-secondary);
        border: 1px solid var(--border);
        border-radius: 8px;
        padding: 6px;
        margin-top: 0.5rem;
        /* Modal-body owns the outer scroll; no inner max-height cap or
           absolute positioning here — used to be a popup overlay when
           the search lived inline at the top of /routing, but it now
           always renders inside the search modal as a normal block. */
    }

    .results-close {
        position: sticky;
        top: 0;
        float: right;
        display: flex;
        align-items: center;
        justify-content: center;
        width: 24px;
        height: 24px;
        border: none;
        background: var(--bg-secondary);
        color: var(--text-muted);
        cursor: pointer;
        border-radius: 4px;
        margin: -2px -2px 0 0;
        z-index: 1;
    }

    .results-close:hover {
        color: var(--text-primary);
        background: var(--bg-tertiary);
    }

    .results-group {
        margin-bottom: 4px;
    }

    .results-group-title {
        padding: 4px 8px;
        font-size: 0.6875rem;
        font-weight: 600;
        color: var(--text-muted);
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
        border: 2px solid var(--border);
        border-top-color: var(--accent);
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
    }

    @keyframes spin {
        to { transform: rotate(360deg); }
    }

    .result-card {
        display: flex;
        align-items: center;
        gap: 10px;
        padding: 10px;
        background: var(--bg-primary);
        border: 1px solid var(--border);
        border-radius: 8px;
        cursor: pointer;
        transition: border-color 0.15s;
        width: 100%;
        text-align: left;
        font: inherit;
        color: inherit;
        margin-bottom: 4px;
    }

    .result-card:hover {
        border-color: var(--accent);
    }

    .result-card:focus-visible {
        outline: 2px solid var(--accent);
        outline-offset: -2px;
    }

    .result-body {
        flex: 1;
        min-width: 0;
    }

    .result-title {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.8125rem;
        font-weight: 600;
    }

    .result-led {
        width: 6px;
        height: 6px;
        border-radius: 50%;
        flex-shrink: 0;
    }

    .led-on {
        background: var(--success, #22c55e);
        box-shadow: 0 0 4px var(--success, #22c55e);
    }

    .led-off {
        background: var(--text-muted);
    }

    .result-name {
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .result-meta {
        display: flex;
        gap: 8px;
        font-size: 0.6875rem;
        color: var(--text-muted);
        margin-top: 1px;
        flex-wrap: wrap;
    }

    .result-tunnel code {
        background: rgba(122,162,247,0.1);
        color: var(--accent);
        padding: 0 4px;
        border-radius: 3px;
        font-size: 0.625rem;
        font-family: var(--font-mono, monospace);
    }

    .result-match {
        font-size: 0.6875rem;
        color: var(--text-secondary);
        margin-top: 2px;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .result-more {
        color: var(--accent);
        font-weight: 500;
    }

    .result-arrow {
        color: var(--text-muted);
        flex-shrink: 0;
        font-size: 1.25rem;
        line-height: 1;
    }

    .resolve-group {
        background: rgba(122,162,247,0.03);
        border-radius: 6px;
        padding: 4px;
    }

    .resolve-ips {
        font-family: var(--font-mono, monospace);
        color: var(--accent);
        font-size: 0.6875rem;
    }

    .result-empty {
        padding: 12px;
        color: var(--text-muted);
        font-style: italic;
        font-size: 0.8125rem;
    }

    .result-error {
        padding: 8px 12px;
        color: var(--error, #ef4444);
        font-size: 0.8125rem;
    }
</style>
