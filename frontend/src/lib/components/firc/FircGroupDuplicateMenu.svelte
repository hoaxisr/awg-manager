<script lang="ts">
	import { m } from '$lib/i18n';
	import { AlertTriangle, Trash2, ArrowUpRight } from 'lucide-svelte';
	import type {
		FircGroupDuplicateConflict,
		FircListUrlOverlap,
	} from '$lib/utils/fircOverlap';

	interface Props {
		conflicts: FircGroupDuplicateConflict[];
		listUrlOverlap?: FircListUrlOverlap;
		shadowedOwnCount: number;
		open: boolean;
		onToggle: () => void;
		onClose: () => void;
		onJumpToRule: (groupId: string, ruleKey: string, rawRuleId: string, isListRule: boolean) => void;
		onJumpToGroup: (groupId: string) => void;
		onRemoveShadowed: () => void;
	}

	let {
		conflicts,
		listUrlOverlap,
		shadowedOwnCount,
		open,
		onToggle,
		onClose,
		onJumpToRule,
		onJumpToGroup,
		onRemoveShadowed,
	}: Props = $props();

	let triggerEl = $state<HTMLButtonElement | null>(null);

	let totalCount = $derived(conflicts.length + (listUrlOverlap ? 1 : 0));
	let hasRuleShadowed = $derived(conflicts.some((c) => c.kind === 'shadowed'));
	let hasShadowed = $derived(hasRuleShadowed || listUrlOverlap?.kind === 'shadowed');
	let overlapKind = $derived<'shadowed' | 'split'>(hasShadowed ? 'shadowed' : 'split');

	let triggerTooltip = $derived.by(() => {
		const parts: string[] = [];
		if (conflicts.length > 0) {
			parts.push(
				hasRuleShadowed
					? m.firc_dup_trigger_shadowed({ count: conflicts.length })
					: m.firc_dup_trigger_split({ count: conflicts.length }),
			);
		}
		if (listUrlOverlap) {
			parts.push(m.firc_dup_trigger_list_url({ group: listUrlOverlap.groupName }));
		}
		return parts.join(' · ') || m.firc_dup_trigger_fallback();
	});

	/**
	 * Portals the popover to `document.body` with `position: fixed` anchored to the trigger
	 * button so ancestor `overflow: hidden` (`.main`) and `opacity` (`.disabled-card`) never
	 * clip or dim the menu.
	 */
	function portalDupPopover(node: HTMLElement) {
		if (typeof document === 'undefined' || typeof window === 'undefined') {
			return {};
		}

		document.body.appendChild(node);

		function updatePosition() {
			if (!triggerEl) return;
			const rect = triggerEl.getBoundingClientRect();
			const vh = window.innerHeight;
			const vw = window.innerWidth;
			const spaceBelow = Math.max(0, vh - rect.bottom - 14);
			const spaceAbove = Math.max(0, rect.top - 14);

			const popWidth = Math.min(420, Math.max(260, vw - 20));
			node.style.position = 'fixed';
			node.style.width = `${popWidth}px`;
			node.style.zIndex = '120';

			let left = rect.left;
			if (left + popWidth > vw - 10) {
				left = vw - 10 - popWidth;
			}
			if (left < 10) {
				left = 10;
			}
			node.style.left = `${left}px`;
			node.style.right = 'auto';

			const maxAvail = Math.max(0, vh - 20);
			const minHeight = Math.min(160, maxAvail);
			const maxEdge = Math.max(10, vh - minHeight - 10);

			if (spaceBelow < 260 && spaceAbove > spaceBelow) {
				const clampedBottom = Math.max(10, Math.min(maxEdge, vh - rect.top + 5));
				node.style.top = 'auto';
				node.style.bottom = `${clampedBottom}px`;
				node.style.maxHeight = `${Math.min(380, Math.max(0, vh - clampedBottom - 10))}px`;
			} else {
				const clampedTop = Math.max(10, Math.min(maxEdge, rect.bottom + 5));
				node.style.top = `${clampedTop}px`;
				node.style.bottom = 'auto';
				node.style.maxHeight = `${Math.min(380, Math.max(0, vh - clampedTop - 10))}px`;
			}
		}

		function handleScroll(e: Event) {
			if (e.target instanceof Node && node.contains(e.target)) return;
			updatePosition();
		}

		updatePosition();
		const raf = requestAnimationFrame(updatePosition);
		window.addEventListener('resize', updatePosition);
		window.addEventListener('scroll', handleScroll, true);

		return {
			destroy() {
				cancelAnimationFrame(raf);
				window.removeEventListener('resize', updatePosition);
				window.removeEventListener('scroll', handleScroll, true);
				if (node.parentNode === document.body) {
					document.body.removeChild(node);
				}
			},
		};
	}

	function conflictSubtitle(c: FircGroupDuplicateConflict): string {
		if (c.inSameGroup) {
			return c.sourceIsListRule
				? m.firc_dup_sub_same_group_list()
				: m.firc_dup_sub_same_group_own();
		}
		if (c.kind === 'shadowed') {
			return m.firc_dup_sub_shadowed({ group: c.sourceGroupName });
		}
		return m.firc_dup_sub_split({ group: c.sourceGroupName });
	}
</script>

{#if totalCount > 0}
	<div class="dup-menu-root">
		<button
			bind:this={triggerEl}
			type="button"
			class="dup-menu-trigger"
			class:is-split={overlapKind === 'split'}
			data-overlap={overlapKind}
			aria-expanded={open}
			aria-label={triggerTooltip}
			title={triggerTooltip}
			onclick={(e) => {
				e.stopPropagation();
				onToggle();
			}}
		>
			<AlertTriangle size={14} />
			<span class="dup-trigger-count">{totalCount}</span>
		</button>

		{#if open}
			<!-- svelte-ignore a11y_click_events_have_key_events -->
			<!-- svelte-ignore a11y_no_static_element_interactions -->
			<div class="dup-popover" use:portalDupPopover onclick={(e) => e.stopPropagation()}>
				<div class="dup-pop-head">
					<span class="dup-pop-title" class:is-split={overlapKind === 'split'}>
						<AlertTriangle size={13} />
						<span>{m.firc_dup_menu_title({ count: totalCount })}</span>
					</span>
					{#if shadowedOwnCount > 0}
						<button
							type="button"
							class="dup-clean-btn"
							title={m.firc_dup_remove_shadowed_tip()}
							onclick={() => {
								onRemoveShadowed();
								onClose();
							}}
						>
							<Trash2 size={12} />
							<span>{m.firc_dup_remove_shadowed_btn({ count: shadowedOwnCount })}</span>
						</button>
					{/if}
				</div>

				{#if listUrlOverlap}
					<div class="dup-url-warn" class:is-split={listUrlOverlap.kind === 'split'}>
						<div class="dup-url-warn-text">
							<span class="dup-url-warn-title">
								{listUrlOverlap.kind === 'shadowed'
									? m.firc_dup_url_shadowed({ group: listUrlOverlap.groupName })
									: m.firc_dup_url_split({ group: listUrlOverlap.groupName })}
							</span>
							<span class="dup-url-warn-sub font-mono">{listUrlOverlap.url}</span>
						</div>
						<button
							type="button"
							class="dup-jump-source-btn"
							title={m.firc_dup_jump_group_tip({ group: listUrlOverlap.groupName })}
							aria-label={m.firc_dup_jump_group_tip({ group: listUrlOverlap.groupName })}
							onclick={() => {
								onJumpToGroup(listUrlOverlap.groupId);
								onClose();
							}}
						>
							<ArrowUpRight size={13} />
						</button>
					</div>
				{/if}

				{#if conflicts.length > 0}
					<div class="dup-list">
						{#each conflicts as c (c.ruleId)}
							<div class="dup-item" class:is-split={c.kind === 'split'}>
								<button
									type="button"
									class="dup-item-main"
									onclick={() => {
										onJumpToRule(c.groupId, c.ruleId, c.rawRuleId, c.isListRule);
										onClose();
									}}
								>
									<div class="dup-item-top">
										<span class="dup-item-type">{c.ruleType}</span>
										<span class="dup-item-pattern font-mono">{c.rulePattern}</span>
									</div>
									<div class="dup-item-sub">
										<span>{conflictSubtitle(c)}</span>
									</div>
								</button>
								<span class="dup-item-count" class:is-split={c.kind === 'split'}>
									×{c.totalRulesWithSameKey}
								</span>
								<button
									type="button"
									class="dup-jump-source-btn"
									title={m.firc_dup_jump_source_tip({ group: c.sourceGroupName })}
									aria-label={m.firc_dup_jump_source_tip({ group: c.sourceGroupName })}
									onclick={() => {
										onJumpToRule(
											c.sourceGroupId,
											c.sourceRuleId,
											c.sourceRawRuleId,
											c.sourceIsListRule,
										);
										onClose();
									}}
								>
									<ArrowUpRight size={13} />
								</button>
							</div>
						{/each}
					</div>
				{/if}
			</div>
		{/if}
	</div>
{/if}

<style>
	.dup-menu-root {
		position: relative;
		display: inline-flex;
		align-items: center;
		flex-shrink: 0;
	}

	.dup-menu-trigger {
		appearance: none;
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 2px 6px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-warning-border);
		background: var(--color-warning-tint);
		color: var(--color-warning);
		font-size: 11px;
		font-weight: 700;
		font-family: var(--font-mono);
		line-height: 1.2;
		cursor: pointer;
		transition:
			filter 0.12s ease,
			border-color 0.12s ease,
			background-color 0.12s ease;
	}

	.dup-menu-trigger:hover {
		filter: brightness(1.12);
	}

	.dup-menu-trigger.is-split {
		color: var(--text-secondary);
		background: var(--bg-tertiary);
		border-color: var(--border-hover, var(--border));
	}

	.dup-trigger-count {
		font-size: 10.5px;
	}

	.dup-popover {
		position: fixed;
		z-index: 120;
		width: 390px;
		max-width: calc(100vw - 24px);
		box-sizing: border-box;
		background: var(--bg-secondary);
		border: 1px solid var(--border-hover, var(--border));
		border-radius: var(--radius);
		box-shadow: var(--shadow);
		padding: 8px;
		display: flex;
		flex-direction: column;
		gap: 6px;
		overflow: hidden;
	}

	.dup-pop-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		padding-bottom: 6px;
		border-bottom: 1px solid var(--border);
		flex-shrink: 0;
	}

	.dup-pop-title {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		font-size: 11.5px;
		font-weight: 700;
		color: var(--color-warning);
	}

	.dup-pop-title.is-split {
		color: var(--text-secondary);
	}

	.dup-clean-btn {
		appearance: none;
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 3px 8px;
		border-radius: var(--radius-sm);
		border: 1px solid color-mix(in srgb, var(--color-error, #f7768e) 40%, var(--border));
		background: color-mix(in srgb, var(--color-error, #f7768e) 12%, transparent);
		color: var(--color-error, #f7768e);
		font-size: 11px;
		font-weight: 600;
		font-family: inherit;
		cursor: pointer;
		white-space: nowrap;
	}

	.dup-clean-btn:hover {
		background: color-mix(in srgb, var(--color-error, #f7768e) 22%, transparent);
	}

	.dup-url-warn {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 6px 8px;
		border-radius: var(--radius-sm);
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		flex-shrink: 0;
	}

	.dup-url-warn.is-split {
		background: var(--bg-tertiary);
		border-color: var(--border);
	}

	.dup-url-warn-text {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.dup-url-warn-title {
		font-size: 11.5px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.dup-url-warn-sub {
		font-size: 10.5px;
		color: var(--text-secondary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.dup-list {
		display: flex;
		flex-direction: column;
		gap: 3px;
		flex: 1 1 auto;
		min-height: 0;
		max-height: 280px;
		overflow-y: auto;
		padding-right: 2px;
	}

	.dup-item {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 4px 6px;
		border-radius: var(--radius-sm);
		border: 1px solid transparent;
		background: var(--bg-primary);
		transition:
			background-color 0.12s ease,
			border-color 0.12s ease;
	}

	.dup-item:hover {
		background: var(--bg-tertiary);
		border-color: var(--border);
	}

	.dup-item-main {
		appearance: none;
		border: 0;
		background: transparent;
		color: inherit;
		font-family: inherit;
		text-align: left;
		flex: 1;
		min-width: 0;
		padding: 0;
		cursor: pointer;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.dup-item-top {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
	}

	.dup-item-type {
		font-size: 10px;
		font-weight: 600;
		text-transform: uppercase;
		padding: 1px 5px;
		border-radius: 4px;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		color: var(--text-secondary);
		flex-shrink: 0;
	}

	.dup-item-pattern {
		font-size: 12px;
		font-weight: 500;
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}

	.dup-item-sub {
		font-size: 11px;
		color: var(--text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.dup-item-count {
		font-size: 10.5px;
		font-weight: 700;
		font-family: var(--font-mono);
		padding: 2px 6px;
		border-radius: 999px;
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		color: var(--color-warning);
		flex-shrink: 0;
	}

	.dup-item-count.is-split {
		background: var(--bg-tertiary);
		border-color: var(--border);
		color: var(--text-secondary);
	}

	.dup-jump-source-btn {
		appearance: none;
		border: 1px solid var(--border);
		background: var(--bg-secondary);
		color: var(--text-secondary);
		border-radius: 4px;
		width: 22px;
		height: 22px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		cursor: pointer;
		flex-shrink: 0;
		padding: 0;
	}

	.dup-jump-source-btn:hover {
		color: var(--text-primary);
		border-color: var(--accent);
		background: var(--bg-tertiary);
	}
</style>
