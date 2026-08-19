<script lang="ts">
	import { untrack } from 'svelte';
	import { api, type SystemServiceItem } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Modal } from '$lib/components/ui';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { Copy, FileCode, Cpu, Check } from 'lucide-svelte';
	import CreateServiceForm from './CreateServiceForm.svelte';
	import {
		buildInitScript,
		DEFAULT_CUSTOM_SCRIPT,
		type CreateMode,
		type CreateServiceFormState,
	} from './createService';

	interface Props {
		open: boolean;
		items: SystemServiceItem[];
		/** Режим, с которого открывается мастер. */
		initialMode: CreateMode;
		/** Служба-донор при открытии из кнопки «Клонировать». */
		donor: SystemServiceItem | null;
		onclose: () => void;
		onCreated: () => Promise<void>;
	}

	let { open, items, initialMode, donor, onclose, onCreated }: Props = $props();

	let form = $state<CreateServiceFormState>({
		mode: 'template',
		tplName: '',
		tplPriority: 90,
		tplDesc: '',
		tplProc: '',
		tplArgs: '',
		cloneSourceScript: '',
		cloneTargetName: '',
		clonePriority: 90,
		customScriptName: '',
		customScriptContent: '',
	});
	let creating = $state(false);

	$effect(() => {
		if (open) untrack(resetForm);
	});

	function resetForm() {
		form.mode = initialMode;
		form.tplName = '';
		form.tplPriority = 90;
		form.tplDesc = '';
		form.tplProc = '';
		form.tplArgs = '';

		if (donor) {
			form.cloneSourceScript = donor.script;
			form.cloneTargetName = donor.name + '-copy';
			form.clonePriority = 90;
		} else if (items.length > 0) {
			form.cloneSourceScript = items[0].script;
			form.cloneTargetName = '';
			form.clonePriority = 90;
		}

		form.customScriptName = 'S90custom-service';
		form.customScriptContent = DEFAULT_CUSTOM_SCRIPT;
	}

	// Dynamic generated script preview for template mode
	const generatedTemplateScript = $derived(
		buildInitScript(
			form.tplProc.trim() || form.tplName.trim() || 'my-daemon',
			form.tplDesc.trim() || form.tplName.trim() || 'Custom Entware Service',
			form.tplArgs.trim(),
		),
	);

	async function handleCreateService() {
		let scriptName = '';
		let content = '';

		if (form.mode === 'template') {
			const clean = form.tplName.trim().replace(/[^a-zA-Z0-9._-]/g, '');
			if (!clean) {
				notifications.error('Укажите корректное имя службы');
				return;
			}
			const prio = Math.min(99, Math.max(10, Number(form.tplPriority) || 90));
			scriptName = `S${prio}${clean}`;
			content = generatedTemplateScript;
		} else if (form.mode === 'clone') {
			const clean = form.cloneTargetName.trim().replace(/[^a-zA-Z0-9._-]/g, '');
			if (!clean) {
				notifications.error('Укажите имя для новой службы');
				return;
			}
			const prio = Math.min(99, Math.max(10, Number(form.clonePriority) || 90));
			scriptName = `S${prio}${clean}`;

			// Fetch donor content
			try {
				const src = await api.systemServicesGet(form.cloneSourceScript);
				content = src.content;
			} catch (e) {
				notifications.error(errorMessage(e, 'Не удалось прочитать исходную службу'));
				return;
			}
		} else {
			// Custom
			scriptName = form.customScriptName.trim();
			if (!scriptName.startsWith('S') || scriptName.length < 4) {
				notifications.error('Имя скрипта должно начинаться с S и номера, например: S90my-service');
				return;
			}
			content = form.customScriptContent;
		}

		creating = true;
		try {
			await api.systemServicesSave({ scriptName, content });
			notifications.success(`Служба ${scriptName} успешно создана!`);
			onclose();
			await onCreated();
		} catch (e) {
			notifications.error(errorMessage(e, 'Ошибка создания службы'));
		} finally {
			creating = false;
		}
	}
</script>

<Modal {open} title="Создание новой службы Entware" size="lg" {onclose}>
	<div class="create-modal-root">
		<!-- Tabs -->
		<div class="modal-mode-tabs">
			<button
				type="button"
				class="mode-tab-btn"
				class:active={form.mode === 'template'}
				onclick={() => (form.mode = 'template')}
			>
				<Cpu size={14} />
				<span>Конструктор (по шаблону)</span>
			</button>
			<button
				type="button"
				class="mode-tab-btn"
				class:active={form.mode === 'clone'}
				onclick={() => (form.mode = 'clone')}
			>
				<Copy size={14} />
				<span>Клонировать службу</span>
			</button>
			<button
				type="button"
				class="mode-tab-btn"
				class:active={form.mode === 'custom'}
				onclick={() => (form.mode = 'custom')}
			>
				<FileCode size={14} />
				<span>Свой bash-скрипт</span>
			</button>
		</div>

		<CreateServiceForm bind:form {items} generatedScript={generatedTemplateScript} />
	</div>

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" onclick={onclose}>Отмена</Button>
			<Button variant="primary" loading={creating} onclick={handleCreateService}>
				{#snippet iconBefore()}<Check size={14} />{/snippet}
				Создать и активировать службу
			</Button>
		</div>
	{/snippet}
</Modal>

<style>
	.create-modal-root {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.modal-mode-tabs {
		display: flex;
		gap: 0.4rem;
		border-bottom: 1px solid var(--color-border);
		padding-bottom: 0.5rem;
	}

	.mode-tab-btn {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		padding: 0.35rem 0.65rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid transparent;
		background: none;
		color: var(--color-text-muted);
		font-size: 0.8rem;
		font-weight: 600;
		cursor: pointer;
	}
	.mode-tab-btn:hover {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}
	.mode-tab-btn.active {
		background: var(--color-accent-tint, rgba(96, 165, 250, 0.15));
		border-color: var(--color-accent);
		color: var(--color-accent);
	}

	.modal-footer-btns {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		width: 100%;
	}
</style>
