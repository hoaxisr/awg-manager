<script lang="ts">
	import { untrack } from 'svelte';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Modal } from '$lib/components/ui';
	import {
		collapseRulesTextToRootDomains,
		parseRulesFromText,
		serializeRulesToText,
	} from '$lib/utils/fircBulkRules';
	import type { FircRule } from '$lib/types/firc';

	interface Props {
		open: boolean;
		groupName?: string;
		rules: FircRule[];
		makeId: () => string;
		onclose: () => void;
		onapply: (nextRules: FircRule[]) => void;
	}

	let { open, groupName = '', rules, makeId, onclose, onapply }: Props = $props();

	let text = $state('');
	let beforeCollapseText = $state<string | null>(null);
	let wasOpen = false;

	$effect(() => {
		if (open && !wasOpen) {
			untrack(() => {
				text = serializeRulesToText(rules);
				beforeCollapseText = null;
			});
		}
		wasOpen = open;
	});

	let previewStats = $derived(parseRulesFromText(text, rules, () => 'preview'));
	let collapsePreview = $derived(collapseRulesTextToRootDomains(text));
	let canCollapse = $derived(
		collapsePreview.collapsedCount > 0 || collapsePreview.deduplicatedCount > 0,
	);

	function handleCollapseToRoots() {
		const res = collapseRulesTextToRootDomains(text);
		if (res.collapsedCount === 0 && res.deduplicatedCount === 0) return;
		beforeCollapseText = text;
		text = res.text;
		notifications.info(
			m.firc_bulk_collapse_done_toast({
				collapsed: res.collapsedCount,
				dupes: res.deduplicatedCount,
			}),
		);
	}

	function handleUndoCollapse() {
		if (beforeCollapseText === null) return;
		text = beforeCollapseText;
		beforeCollapseText = null;
	}

	function handleApply() {
		const parsed = parseRulesFromText(text, rules, makeId);
		onapply(parsed.rules);
	}
</script>

<Modal
	{open}
	{onclose}
	title={groupName
		? m.firc_bulk_rules_editor_title({ name: groupName })
		: m.firc_bulk_rules_editor_title_default()}
	size="lg"
	width="760px"
>
	<div class="br-body">
		<p class="br-hint">{m.firc_bulk_rules_editor_hint()}</p>

		<textarea
			class="br-textarea"
			rows={16}
			aria-label={m.firc_bulk_rules_edit_list_btn()}
			placeholder={m.firc_bulk_rules_editor_placeholder()}
			bind:value={text}
		></textarea>

		<div class="br-toolbar">
			<div class="br-stats-pill" data-testid="firc-bulk-rules-editor-stats">
				{m.firc_bulk_rules_editor_stats({
					total: previewStats.rules.length,
					added: previewStats.added,
					removed: previewStats.removed,
					disabled: previewStats.disabled,
				})}
			</div>

			<div class="br-toolbar-actions">
				{#if beforeCollapseText !== null}
					<Button variant="ghost" size="sm" onclick={handleUndoCollapse}>
						{m.firc_bulk_collapse_undo_btn()}
					</Button>
				{/if}
				<Button
					variant="secondary"
					size="sm"
					disabled={!canCollapse}
					title={m.firc_bulk_collapse_root_title()}
					onclick={handleCollapseToRoots}
				>
					{m.firc_bulk_collapse_root_btn()}
				</Button>
			</div>
		</div>
	</div>

	{#snippet actions()}
		<div class="br-footer-btns">
			<Button variant="ghost" size="sm" onclick={onclose}>{m.common_cancel()}</Button>
			<Button variant="primary" size="sm" onclick={handleApply}>{m.common_apply()}</Button>
		</div>
	{/snippet}
</Modal>

<style>
	.br-body {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.br-hint {
		margin: 0;
		font-size: 12.5px;
		line-height: 1.45;
		color: var(--color-text-secondary);
	}

	.br-textarea {
		width: 100%;
		min-height: 280px;
		padding: 10px 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-family: var(--font-mono);
		font-size: 12.5px;
		line-height: 1.5;
		resize: vertical;
		outline: none;
		box-sizing: border-box;
		transition: border-color var(--t-fast);
	}

	.br-textarea:focus {
		border-color: var(--color-accent);
	}

	.br-toolbar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
	}

	.br-toolbar-actions {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}

	.br-stats-pill {
		font-family: var(--font-mono);
		font-size: 11.5px;
		padding: 4px 10px;
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
	}

	.br-footer-btns {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
		width: 100%;
	}
</style>
