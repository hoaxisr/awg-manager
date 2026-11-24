<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Badge, Toggle, Modal } from '$lib/components/ui';
	import { LayoutGrid } from 'lucide-svelte';
	import { searchFircCatalog } from '$lib/utils/fircCatalogSearch';
	import { intervalOptions, type GroupForm } from '$lib/utils/fircGroups';
	import type { FircCatalog, FircListPreviewRes } from '$lib/types/firc';

	interface Props {
		open: boolean;
		mode: 'create' | 'edit';
		form: GroupForm;
		availableInterfaces: { value: string; label: string }[];
		/** Блок проверки доступности: только у цепочки Fallback из 2+ интерфейсов без привязанной прокси-группы. */
		showCheck: boolean;
		/** Имя прокси-группы, если проверка цепочки управляется через неё. */
		managedProxyGroupName?: string;
		catalog: FircCatalog | null;
		previewLoading: boolean;
		previewData: FircListPreviewRes | null;
		previewError: string | null;
		onclose: () => void;
		onsubmit: () => void;
		onpreview: () => void;
		onensureCatalog: () => void;
		onopenCatalog: () => void;
	}

	let {
		open,
		mode,
		form = $bindable(),
		availableInterfaces,
		showCheck,
		managedProxyGroupName = '',
		catalog,
		previewLoading,
		previewData,
		previewError,
		onclose,
		onsubmit,
		onpreview,
		onensureCatalog,
		onopenCatalog,
	}: Props = $props();

	let catalogSearch = $derived.by(() =>
		form.listEnabled ? searchFircCatalog(catalog, form.listUrl, 100) : { items: [], total: 0 },
	);
	let catalogMatches = $derived(catalogSearch.items);
</script>

<!-- Modal 1: Group Create / Settings -->
<Modal
	{open}
	{onclose}
	title={mode === 'create' ? m.firc_groups_modal_create_title() : m.firc_groups_modal_edit_title()}
	size="lg"
>
	<div class="modal-form">
		<div class="form-grid-2">
			<div class="field">
				<label class="lbl" for="firc-gm-name">{m.firc_groups_form_name()}</label>
				<input
					id="firc-gm-name"
					type="text"
					class="inp"
					placeholder={m.firc_groups_form_name_placeholder()}
					bind:value={form.name}
				/>
			</div>
			<div class="field">
				<label class="lbl" for="firc-gm-iface">{m.firc_groups_form_iface()}</label>
				<select id="firc-gm-iface" class="inp" bind:value={form.iface}>
					{#each availableInterfaces as opt}
						<option value={opt.value}>{opt.label}</option>
					{/each}
				</select>
			</div>
		</div>

		<div class="sec-box">
			<div class="field">
				<label class="lbl" for="firc-gm-dns">{m.firc_groups_form_dns()}</label>
				<select
					id="firc-gm-dns"
					class="inp"
					bind:value={form.dnsMode}
					disabled={form.iface === 'block'}
				>
					<option value="auto">{m.firc_groups_form_dns_auto()}</option>
					<option value="own">{m.firc_groups_form_dns_own()}</option>
					<option value="off">{m.firc_groups_form_dns_off()}</option>
				</select>
			</div>
			{#if form.dnsMode === 'own' && form.iface !== 'block'}
				<div class="field mt-2">
					<label class="lbl" for="firc-gm-dns-srv">{m.firc_groups_form_dns_servers()}</label>
					<input
						id="firc-gm-dns-srv"
						type="text"
						class="inp font-mono"
						placeholder="1.1.1.1, 8.8.8.8"
						bind:value={form.dnsServer}
					/>
					<p class="hint">
						{m.firc_groups_form_dns_servers_hint()}
					</p>
				</div>
			{/if}
		</div>

		{#if showCheck}
			<div class="sec-box">
				<div class="field">
					<label class="lbl" for="firc-gm-check-mode">{m.firc_group_check_label()}</label>
					<select id="firc-gm-check-mode" class="inp" bind:value={form.checkMode}>
						<option value="url">{m.firc_group_check_mode_url()}</option>
						<option value="keendns">KeenDNS Cloud relay TCP</option>
						<option value="none">{m.firc_group_check_mode_none()}</option>
					</select>
				</div>
				{#if form.checkMode === 'url'}
					<div class="field mt-2">
						<label class="lbl" for="firc-gm-check-url">{m.firc_group_check_url_label()}</label>
						<input
							id="firc-gm-check-url"
							type="text"
							class="inp font-mono"
							placeholder={m.firc_group_check_url_placeholder()}
							bind:value={form.checkUrl}
						/>
						<p class="hint">
							{m.firc_group_check_url_hint()}
						</p>
					</div>
				{/if}
			</div>
		{:else if managedProxyGroupName}
			<div class="sec-box">
				<p class="hint">
					{m.firc_group_check_managed_by_pg({ name: managedProxyGroupName })}
				</p>
			</div>
		{/if}

		<div class="sec-box">
			<div class="field-row">
				<div class="field-row-info">
					<div class="field-title">{m.firc_groups_form_list_title()}</div>
					<div class="hint">{m.firc_groups_form_list_hint()}</div>
				</div>
				<div class="list-toggle-actions">
					<Button variant="secondary" size="sm" onclick={onopenCatalog}>
						{#snippet iconBefore()}<LayoutGrid size={13} />{/snippet}
						{m.firc_groups_service_catalog()}
					</Button>
					<Toggle
						checked={form.listEnabled}
						onchange={(v) => { form.listEnabled = v; if (v) onensureCatalog(); }}
						ariaLabel={m.firc_groups_form_list_toggle_aria()}
					/>
				</div>
			</div>

			{#if form.listEnabled}
				<div class="field mt-2">
					<label class="lbl" for="firc-gm-url">{m.firc_groups_form_list_url()}</label>
					<input
						id="firc-gm-url"
						type="text"
						class="inp font-mono"
						placeholder={m.firc_groups_form_list_url_placeholder()}
						bind:value={form.listUrl}
					/>
					{#if catalogMatches.length > 0}
						<div class="catalog-dropdown">
							{#each catalogMatches as hit (hit.url)}
								<button
									type="button"
									class="catalog-item"
									onclick={() => (form.listUrl = hit.url)}
								>
									<span class="font-mono">{hit.name}</span>
									<Badge variant="muted" size="sm">{hit.source}</Badge>
								</button>
							{/each}
							{#if catalogSearch.total > catalogMatches.length}
								<div class="catalog-more-hint">
									{m.firc_catalog_more_hint({ shown: catalogMatches.length, total: catalogSearch.total })}
								</div>
							{/if}
						</div>
					{/if}
				</div>

				<div class="form-grid-2 mt-2">
					<div class="field">
						<label class="lbl" for="firc-gm-interval">{m.firc_groups_form_interval()}</label>
						<select id="firc-gm-interval" class="inp" bind:value={form.listInterval}>
							{#each intervalOptions() as opt}
								<option value={opt.value}>{opt.label}</option>
							{/each}
						</select>
					</div>
					<div class="field" style="justify-content: flex-end;">
						<Button
							variant="secondary"
							size="sm"
							loading={previewLoading}
							onclick={onpreview}
						>
							{m.firc_groups_form_preview()}
						</Button>
					</div>
				</div>

				{#if previewError}
					<div class="preview-err">{previewError}</div>
				{:else if previewData}
					<div class="preview-box">
						<div>
							{m.firc_groups_form_preview_count()} <strong>{previewData.total ?? previewData.rules.length}</strong>
						</div>
						{#if previewData.rules.length > 0}
							<div class="preview-sample font-mono">
								{#each previewData.rules.slice(0, 8) as pr}
									<div>[{pr.type}] {pr.rule}</div>
								{/each}
							</div>
						{/if}
					</div>
				{/if}
			{/if}
		</div>
	</div>

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" size="sm" onclick={onclose}>{m.common_cancel()}</Button>
			<Button variant="primary" size="sm" onclick={onsubmit}>
				{mode === 'create' ? m.firc_groups_modal_create_btn() : m.common_apply()}
			</Button>
		</div>
	{/snippet}
</Modal>

<style>
	.inp {
		padding: 5px 9px;
		border-radius: var(--radius-sm);
		background: var(--bg-primary);
		border: 1px solid var(--border);
		color: var(--text-primary);
		font-size: 12.5px;
		font-family: inherit;
	}

	.inp:focus {
		outline: none;
		border-color: var(--accent);
	}

	.modal-form {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.form-grid-2 {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 10px;
	}

	.sec-box {
		padding: 10px 12px;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.field-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 10px 12px;
		min-width: 0;
	}

	.field-row-info {
		flex: 1 1 180px;
		min-width: 0;
	}

	.field-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.lbl {
		font-size: 11.5px;
		color: var(--text-secondary);
		font-weight: 500;
	}

	.hint {
		margin: 0;
		font-size: 11.5px;
		color: var(--text-muted);
		line-height: 1.4;
	}

	.catalog-dropdown {
		max-height: 220px;
		overflow-y: auto;
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		background: var(--bg-secondary);
		display: flex;
		flex-direction: column;
	}

	.catalog-more-hint {
		padding: 5px 10px;
		font-size: 11px;
		color: var(--text-muted);
		border-top: 1px solid var(--border);
		background: var(--bg-tertiary);
	}

	.catalog-item {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 5px 10px;
		border: 0;
		background: transparent;
		color: var(--text-primary);
		font-size: 12px;
		cursor: pointer;
		text-align: left;
	}

	.catalog-item:hover {
		background: var(--bg-tertiary);
	}

	.preview-err {
		font-size: 12px;
		color: var(--color-danger);
	}

	.preview-box {
		font-size: 12px;
		color: var(--text-secondary);
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.preview-sample {
		max-height: 120px;
		overflow-y: auto;
		padding: 6px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 4px;
		font-size: 11px;
	}

	.modal-footer-btns {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
		width: 100%;
	}

	.mt-2 {
		margin-top: 8px;
	}

	.list-toggle-actions {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		flex-shrink: 0;
	}

	@media (max-width: 768px) {
		.form-grid-2 {
			grid-template-columns: 1fr;
		}
	}
</style>
