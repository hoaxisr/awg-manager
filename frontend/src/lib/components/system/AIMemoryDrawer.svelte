<script lang="ts">
	import { Modal, Button, Badge } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { AIMemoryData, AIMemoryFact, AILearnedPlaybook, AILearningJournalEntry, AISentinelSettings } from '$lib/types';
	import {
		Brain,
		Sparkles,
		ShieldCheck,
		ShieldAlert,
		BookOpen,
		History,
		Trash2,
		Plus,
		RefreshCw,
		CheckCircle2,
		AlertTriangle,
		Bot,
		Cpu,
		Clock,
		ExternalLink,
		Zap,
		ChevronRight,
		Check,
		Info,
	} from 'lucide-svelte';

	interface Props {
		open: boolean;
		onClose: () => void;
	}

	let { open, onClose }: Props = $props();

	let activeTab = $state<'facts' | 'playbooks' | 'journal' | 'sentinel'>('facts');
	let loading = $state(false);
	let error = $state<string | null>(null);
	let memoryData = $state<AIMemoryData | null>(null);

	// New fact form
	let newFactCategory = $state('network');
	let newFactContent = $state('');
	let addingFact = $state(false);

	// Sentinel settings edit
	let sentinelEnabled = $state(true);
	let sentinelAutonomy = $state<'disabled' | 'notify_only' | 'safe_auto'>('notify_only');
	let sentinelInterval = $state(60);
	let savingSentinel = $state(false);

	$effect(() => {
		if (open) {
			loadData();
		}
	});

	async function loadData() {
		loading = true;
		error = null;
		try {
			const res = await api.systemAIMemory();
			memoryData = res;
			if (res.settings) {
				sentinelEnabled = res.settings.enabled;
				sentinelAutonomy = (res.settings.autonomyLevel as 'disabled' | 'notify_only' | 'safe_auto') || 'notify_only';
				sentinelInterval = res.settings.intervalSeconds || 60;
			}
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	}

	async function addFact() {
		if (!newFactContent.trim()) return;
		addingFact = true;
		try {
			await api.systemAIAddFact({
				category: newFactCategory,
				content: newFactContent.trim(),
				source: 'user',
			});
			newFactContent = '';
			notifications.success('Факт успешно сохранён в память ассистента');
			await loadData();
		} catch (e) {
			notifications.error('Ошибка сохранения: ' + (e instanceof Error ? e.message : String(e)));
		} finally {
			addingFact = false;
		}
	}

	async function deleteFact(id: string) {
		try {
			await api.systemAIDeleteFact(id);
			notifications.success('Факт удалён');
			await loadData();
		} catch (e) {
			notifications.error('Ошибка удаления: ' + (e instanceof Error ? e.message : String(e)));
		}
	}

	async function deletePlaybook(id: string) {
		try {
			await api.systemAIDeletePlaybook(id);
			notifications.success('Playbook удалён');
			await loadData();
		} catch (e) {
			notifications.error('Ошибка удаления: ' + (e instanceof Error ? e.message : String(e)));
		}
	}

	async function saveSentinel() {
		savingSentinel = true;
		try {
			await api.systemAISaveSentinel({
				enabled: sentinelEnabled,
				autonomyLevel: sentinelAutonomy,
				intervalSeconds: Number(sentinelInterval) || 60,
			});
			notifications.success('Настройки Sentinel сохранены');
			await loadData();
		} catch (e) {
			notifications.error('Ошибка сохранения: ' + (e instanceof Error ? e.message : String(e)));
		} finally {
			savingSentinel = false;
		}
	}

	function formatDate(d: string | undefined): string {
		if (!d) return '—';
		try {
			const date = new Date(d);
			return date.toLocaleString('ru-RU', {
				day: 'numeric',
				month: 'short',
				hour: '2-digit',
				minute: '2-digit',
			});
		} catch {
			return d;
		}
	}

	function getCategoryBadgeVariant(cat: string): 'info' | 'purple' | 'warning' | 'success' | 'error' | 'default' {
		switch (cat) {
			case 'network': return 'info';
			case 'routing': return 'purple';
			case 'device': return 'warning';
			case 'user_pref': return 'success';
			case 'troubleshooting': return 'error';
			default: return 'default';
		}
	}

	function getCategoryLabel(cat: string): string {
		switch (cat) {
			case 'network': return 'Сеть / LAN';
			case 'routing': return 'Маршрутизация';
			case 'device': return 'Устройство';
			case 'user_pref': return 'Предпочтение';
			case 'troubleshooting': return 'Диагностика';
			case 'tunnel': return 'Туннели';
			case 'system': return 'Система';
			default: return cat;
		}
	}
</script>

<Modal
	{open}
	onclose={onClose}
	title="Память и самообучение ИИ"
	size="wide"
	bodyMinHeight="480px"
>
	<div class="memory-container">
		<!-- Sentinel Status Banner -->
		<div class="sentinel-header-banner">
			<div class="sentinel-info">
				<div class="sentinel-icon-wrap">
					<Brain class="w-5 h-5 text-accent" />
				</div>
				<div>
					<div class="flex items-center gap-2">
						<span class="font-semibold text-sm text-foreground">Фоновый часовой (Sentinel)</span>
						{#if memoryData?.sentinel?.running && memoryData?.sentinel?.enabled && memoryData?.sentinel?.autonomyLevel !== 'disabled'}
							<Badge variant="success" size="xs" pill>
								<span class="status-dot-pulse"></span>
								Активен ({sentinelAutonomy === 'safe_auto' ? 'Авто-исправление' : 'Рекомендации'})
							</Badge>
						{:else}
							<Badge variant="muted" size="xs">
								Выключен
							</Badge>
						{/if}
					</div>
					<div class="text-xs text-muted-foreground mt-0.5 flex flex-wrap items-center gap-2">
						<span>
							{#if memoryData?.sentinel?.lastCheck}
								Последняя проверка: {formatDate(memoryData.sentinel.lastCheck)}
							{:else}
								Ожидание первого цикла проверки
							{/if}
						</span>
						{#if memoryData?.sentinel?.lastAction}
							<span class="text-foreground font-mono bg-bg-tertiary px-1.5 py-0.5 rounded text-[11px]">
								{memoryData.sentinel.lastAction}
							</span>
						{/if}
					</div>
				</div>
			</div>
			<Button variant="ghost" size="sm" onclick={loadData} disabled={loading} title="Обновить данные памяти">
				<RefreshCw class="w-4 h-4 {loading ? 'animate-spin' : ''}" />
			</Button>
		</div>

		<!-- Tabs Bar -->
		<div class="memory-tabs">
			<button
				type="button"
				class="memory-tab-btn"
				class:active={activeTab === 'facts'}
				onclick={() => (activeTab = 'facts')}
			>
				<BookOpen class="w-4 h-4" />
				<span>База фактов</span>
				<span class="tab-count-pill">{memoryData?.facts?.length || 0}</span>
			</button>

			<button
				type="button"
				class="memory-tab-btn"
				class:active={activeTab === 'playbooks'}
				onclick={() => (activeTab = 'playbooks')}
			>
				<Sparkles class="w-4 h-4" />
				<span>Playbooks (Рецепты)</span>
				<span class="tab-count-pill">{memoryData?.playbooks?.length || 0}</span>
			</button>

			<button
				type="button"
				class="memory-tab-btn"
				class:active={activeTab === 'journal'}
				onclick={() => (activeTab = 'journal')}
			>
				<History class="w-4 h-4" />
				<span>Журнал обучения</span>
				<span class="tab-count-pill">{memoryData?.journal?.length || 0}</span>
			</button>

			<button
				type="button"
				class="memory-tab-btn"
				class:active={activeTab === 'sentinel'}
				onclick={() => (activeTab = 'sentinel')}
			>
				<ShieldCheck class="w-4 h-4" />
				<span>Автономия</span>
			</button>
		</div>

		{#if error}
			<div class="error-banner">
				<AlertTriangle class="w-4 h-4 flex-shrink-0" />
				<span>{error}</span>
			</div>
		{/if}

		<!-- TAB 1: FACTS -->
		{#if activeTab === 'facts'}
			<div class="tab-pane">
				<!-- Add Fact Form -->
				<div class="fact-form-card">
					<div class="form-title">
						<Plus class="w-4 h-4 text-accent" />
						<span>Запомнить новый факт о роутере, сети или топологии</span>
					</div>

					<div class="w-full">
						<input
							type="text"
							placeholder="Например: Домашняя сеть 192.168.90.0/24, дача 192.168.50.0/24 через SSTP"
							bind:value={newFactContent}
							onkeydown={(e) => e.key === 'Enter' && addFact()}
							class="fact-input"
						/>
					</div>

					<div class="form-action-row">
						<div class="flex items-center gap-2">
							<span class="text-xs text-muted-foreground">Категория:</span>
							<select
								bind:value={newFactCategory}
								class="category-select"
							>
								<option value="network">Сеть / LAN</option>
								<option value="routing">Маршрутизация</option>
								<option value="device">Устройство / Keenetic</option>
								<option value="user_pref">Предпочтение пользователя</option>
								<option value="troubleshooting">Диагностика</option>
							</select>
						</div>

						<Button variant="primary" size="sm" onclick={addFact} disabled={addingFact || !newFactContent.trim()}>
							<Plus class="w-3.5 h-3.5 mr-1" />
							Запомнить факт
						</Button>
					</div>

					<p class="form-hint">
						<Info class="w-3.5 h-3.5 inline-block mr-1 opacity-70" />
						Факты автоматически внедряются в системный контекст модели при каждом запросе, позволяя ассистенту знать особенности и топологию вашей сети.
					</p>
				</div>

				<!-- Facts List -->
				{#if !memoryData?.facts || memoryData.facts.length === 0}
					<div class="empty-state">
						<BookOpen class="w-8 h-8 opacity-40 mb-2" />
						<p class="font-medium text-foreground">В памяти пока нет сохранённых фактов</p>
						<p class="text-xs text-muted-foreground mt-1">
							Ассистент запоминает факты автоматически в ходе диалога, либо вы можете добавить факт в форме выше.
						</p>
					</div>
				{:else}
					<div class="facts-list">
						{#each memoryData.facts as fact (fact.id)}
							<div class="fact-card">
								<div class="fact-header">
									<div class="flex items-center gap-2">
										<Badge variant={getCategoryBadgeVariant(fact.category)} size="xs" uppercase>
											{getCategoryLabel(fact.category)}
										</Badge>
										<span class="text-[11px] text-muted-foreground">
											{formatDate(fact.updatedAt || fact.createdAt)}
										</span>
										{#if fact.source === 'user'}
											<Badge variant="muted" size="xs">пользователь</Badge>
										{:else}
											<Badge variant="muted" size="xs">ассистент</Badge>
										{/if}
									</div>

									<button
										type="button"
										class="delete-btn"
										title="Удалить факт"
										onclick={() => deleteFact(fact.id)}
									>
										<Trash2 class="w-3.5 h-3.5" />
									</button>
								</div>
								<div class="fact-content">{fact.content}</div>
							</div>
						{/each}
					</div>
				{/if}
			</div>
		{/if}

		<!-- TAB 2: PLAYBOOKS -->
		{#if activeTab === 'playbooks'}
			<div class="tab-pane">
				<div class="section-intro">
					<Sparkles class="w-4 h-4 text-accent" />
					<span>
						<strong>Выученные сценарии решений (Playbooks).</strong> При успешном подтверждении и проверке устранения проблемы ассистент автоматически сохраняет рецепт для быстрого устранения похожих сбоев.
					</span>
				</div>

				{#if !memoryData?.playbooks || memoryData.playbooks.length === 0}
					<div class="empty-state">
						<Sparkles class="w-8 h-8 opacity-40 mb-2" />
						<p class="font-medium text-foreground">Пока нет выученных сценариев решений</p>
						<p class="text-xs text-muted-foreground mt-1 max-w-lg">
							Когда ИИ-ассистент или Sentinel успешно устраняют проблему (например, добавление обратного маршрута в KeeneticOS, перезапуск сервиса или перезагрузка ядра), проверенный сценарий появится здесь.
						</p>
					</div>
				{:else}
					<div class="playbooks-list">
						{#each memoryData.playbooks as pb (pb.id)}
							<div class="playbook-card">
								<div class="playbook-header">
									<div class="flex items-center gap-2 flex-wrap">
										<span class="playbook-title">{pb.title || pb.trigger || 'Сценарий'}</span>
										<Badge variant={getCategoryBadgeVariant(pb.category)} size="xs" uppercase>
											{getCategoryLabel(pb.category)}
										</Badge>
										<Badge variant="success" size="xs">
											✓ Успешно: {pb.successCount}
										</Badge>
									</div>
									<button
										type="button"
										class="delete-btn"
										title="Удалить playbook"
										onclick={() => deletePlaybook(pb.id)}
									>
										<Trash2 class="w-3.5 h-3.5" />
									</button>
								</div>

								<div class="playbook-body">
									<div class="symptom-row">
										<span class="label">Симптом:</span>
										<code class="code-pill">{pb.trigger}</code>
									</div>

									{#if pb.diagnosis}
										<div class="diag-row">
											<span class="label">Диагноз:</span>
											<span class="text-foreground/90">{pb.diagnosis}</span>
										</div>
									{/if}

									<div class="action-row">
										<span class="label">Действие:</span>
										<code class="action-pill">{pb.action}{pb.target ? ` (${pb.target})` : ''}</code>
									</div>
								</div>

								<div class="playbook-footer">
									<span>Источник: {pb.learnedFrom || 'ai_assistant'}</span>
									{#if pb.lastUsedAt}
										<span>Использован: {formatDate(pb.lastUsedAt)}</span>
									{:else if pb.createdAt}
										<span>Создан: {formatDate(pb.createdAt)}</span>
									{/if}
								</div>
							</div>
						{/each}
					</div>
				{/if}
			</div>
		{/if}

		<!-- TAB 3: JOURNAL -->
		{#if activeTab === 'journal'}
			<div class="tab-pane">
				<div class="section-intro">
					<History class="w-4 h-4 text-accent" />
					<span>
						<strong>Журнал самообучения.</strong> Хроника диагностических консультаций, предложенных решений и автоматических исправлений.
					</span>
				</div>

				{#if !memoryData?.journal || memoryData.journal.length === 0}
					<div class="empty-state">
						<History class="w-8 h-8 opacity-40 mb-2" />
						<p class="font-medium text-foreground">Журнал обучения пока пуст</p>
						<p class="text-xs text-muted-foreground mt-1">
							События анализа, запросы к модели и результаты применения действий будут логироваться сюда.
						</p>
					</div>
				{:else}
					<div class="journal-timeline">
						{#each memoryData.journal as entry (entry.id)}
							<div class="journal-entry">
								<div class="journal-top">
									<div class="flex items-center gap-2 flex-wrap">
										{#if entry.outcome === 'success'}
											<Badge variant="success" size="xs">УСПЕХ</Badge>
										{:else if entry.outcome === 'failed'}
											<Badge variant="error" size="xs">СБОЙ</Badge>
										{:else if entry.outcome === 'learned'}
											<Badge variant="purple" size="xs">ВЫУЧЕНО</Badge>
										{:else}
											<Badge variant="muted" size="xs">{entry.outcome || 'ЗАПИСЬ'}</Badge>
										{/if}
										<code class="trigger-code">{entry.trigger}</code>
									</div>
									<span class="text-xs text-muted-foreground">{formatDate(entry.timestamp)}</span>
								</div>

								{#if entry.query && entry.query !== entry.trigger}
									<div class="journal-query">
										<span class="text-muted-foreground">Вопрос:</span> {entry.query}
									</div>
								{/if}

								{#if entry.cloudAdvice}
									<div class="journal-advice">
										<div class="text-[11px] text-muted-foreground mb-0.5">Диагноз ИИ:</div>
										<div class="text-foreground/90">{entry.cloudAdvice}</div>
									</div>
								{/if}

								{#if entry.actionTaken}
									<div class="journal-action">
										<span class="text-muted-foreground">Применено:</span>
										<code class="action-pill">{entry.actionTaken}</code>
									</div>
								{/if}
							</div>
						{/each}
					</div>
				{/if}
			</div>
		{/if}

		<!-- TAB 4: SENTINEL SETTINGS -->
		{#if activeTab === 'sentinel'}
			<div class="tab-pane">
				<div class="sentinel-settings-card">
					<div class="toggle-row">
						<div>
							<div class="font-semibold text-sm text-foreground">Включить фонового часового (Sentinel)</div>
							<div class="text-xs text-muted-foreground mt-0.5">
								Регулярная фоновая проверка доступности туннелей, маршрутизации и ключевых служб роутера
							</div>
						</div>
						<input
							type="checkbox"
							bind:checked={sentinelEnabled}
							class="toggle-checkbox"
						/>
					</div>

					<div class="settings-divider"></div>

					<div class="space-y-2">
						<div class="font-semibold text-xs text-foreground uppercase tracking-wider">
							Режим автономности
						</div>

						<div class="autonomy-options">
							<label class="autonomy-card" class:selected={sentinelAutonomy === 'notify_only'}>
								<input
									type="radio"
									name="autonomy"
									value="notify_only"
									bind:group={sentinelAutonomy}
									class="mt-1"
								/>
								<div class="autonomy-content">
									<div class="flex items-center gap-2">
										<span class="font-medium text-sm text-foreground">Только рекомендации</span>
										<Badge variant="accent" size="xs">Рекомендуется</Badge>
									</div>
									<div class="text-xs text-muted-foreground mt-1">
										При обнаружении сбоя Sentinel формирует диагноз и выводит в интерфейсе кнопку исправления, ожидая вашего клика. Никаких изменений без вашего подтверждения.
									</div>
								</div>
							</label>

							<label class="autonomy-card" class:selected={sentinelAutonomy === 'safe_auto'}>
								<input
									type="radio"
									name="autonomy"
									value="safe_auto"
									bind:group={sentinelAutonomy}
									class="mt-1"
								/>
								<div class="autonomy-content">
									<div class="flex items-center gap-2">
										<span class="font-medium text-sm text-foreground">Безопасное авто-исправление (Safe Auto)</span>
										<Badge variant="warning" size="xs">Автономный</Badge>
									</div>
									<div class="text-xs text-muted-foreground mt-1">
										Автоматически применяет действия для типовых сбоев, если для них уже существует успешный выученный Playbook с подтверждённым успехом.
									</div>
								</div>
							</label>

							<label class="autonomy-card" class:selected={sentinelAutonomy === 'disabled'}>
								<input
									type="radio"
									name="autonomy"
									value="disabled"
									bind:group={sentinelAutonomy}
									class="mt-1"
								/>
								<div class="autonomy-content">
									<div class="font-medium text-sm text-foreground">Отключено</div>
									<div class="text-xs text-muted-foreground mt-1">
										Sentinel не выполняет фоновых проверок и не потребляет ресурсы роутера.
									</div>
								</div>
							</label>
						</div>
					</div>

					<div class="settings-divider"></div>

					<div class="space-y-2">
						<label for="sentinel-interval" class="font-semibold text-xs text-foreground uppercase tracking-wider block">
							Интервал фоновых проверок (секунд)
						</label>
						<div class="flex items-center gap-3">
							<input
								id="sentinel-interval"
								type="number"
								min="10"
								max="600"
								bind:value={sentinelInterval}
								class="interval-input"
							/>
							<span class="text-xs text-muted-foreground">
								По умолчанию 60 секунд. Проверки очень легковесны и безопасны для процессора роутера.
							</span>
						</div>
					</div>

					<div class="pt-2 flex justify-end">
						<Button variant="primary" size="md" onclick={saveSentinel} disabled={savingSentinel}>
							<CheckCircle2 class="w-4 h-4 mr-1.5" />
							Сохранить параметры Sentinel
						</Button>
					</div>
				</div>
			</div>
		{/if}
	</div>
</Modal>

<style>
	.memory-container {
		display: flex;
		flex-direction: column;
		gap: 1rem;
		color: var(--color-text-primary);
		width: 100%;
	}

	.sentinel-header-banner {
		padding: 0.875rem 1rem;
		border-radius: var(--radius, 10px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
	}

	.sentinel-info {
		display: flex;
		align-items: center;
		gap: 0.875rem;
	}

	.sentinel-icon-wrap {
		width: 2.25rem;
		height: 2.25rem;
		border-radius: var(--radius-sm, 6px);
		background: var(--color-accent-tint);
		display: flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
	}

	.status-dot-pulse {
		display: inline-block;
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--color-success);
		margin-right: 4px;
		animation: pulse-dot 2s infinite ease-in-out;
	}

	@keyframes pulse-dot {
		0%, 100% { opacity: 1; transform: scale(1); }
		50% { opacity: 0.4; transform: scale(0.85); }
	}

	.memory-tabs {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		border-bottom: 1px solid var(--color-border);
		padding-bottom: 0.5rem;
		overflow-x: auto;
	}

	.memory-tab-btn {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.4375rem 0.875rem;
		border-radius: var(--radius-sm, 6px);
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--color-text-secondary);
		background: transparent;
		border: 1px solid transparent;
		cursor: pointer;
		transition: all 0.15s ease;
		white-space: nowrap;
	}

	.memory-tab-btn:hover {
		color: var(--color-text-primary);
		background: var(--color-bg-hover);
	}

	.memory-tab-btn.active {
		color: var(--color-accent);
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
		font-weight: 600;
	}

	.tab-count-pill {
		font-size: 10px;
		font-weight: 600;
		padding: 1px 6px;
		border-radius: var(--radius-pill, 9999px);
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}

	.tab-pane {
		display: flex;
		flex-direction: column;
		gap: 1rem;
		min-height: 380px;
	}

	.error-banner {
		padding: 0.75rem 1rem;
		border-radius: var(--radius-sm, 6px);
		background: var(--color-error-tint);
		border: 1px solid var(--color-error-border);
		color: var(--color-error);
		font-size: 0.8125rem;
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.section-intro {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		padding: 0.625rem 0.875rem;
		border-radius: var(--radius-sm, 6px);
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		font-size: 0.78125rem;
		color: var(--color-text-secondary);
		line-height: 1.4;
	}

	.empty-state {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		padding: 3.5rem 1.5rem;
		text-align: center;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius, 10px);
		background: var(--color-bg-tertiary);
	}

	/* Form */
	.fact-form-card {
		padding: 1rem;
		border-radius: var(--radius, 10px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.form-title {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-weight: 600;
		font-size: 0.8125rem;
		color: var(--color-text-primary);
	}

	.fact-input {
		width: 100%;
		height: 2.375rem;
		padding: 0 0.75rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-size: 0.8125rem;
		outline: none;
		transition: border-color 0.15s ease, box-shadow 0.15s ease;
		box-sizing: border-box;
	}

	.fact-input:focus {
		border-color: var(--color-accent);
		box-shadow: 0 0 0 2px var(--color-accent-tint);
	}

	.form-action-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		flex-wrap: wrap;
	}

	.category-select {
		height: 2rem;
		padding: 0 0.625rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-size: 0.78125rem;
		outline: none;
		cursor: pointer;
	}

	.form-hint {
		font-size: 0.71875rem;
		color: var(--color-text-muted);
		line-height: 1.4;
	}

	/* Fact card */
	.facts-list {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.fact-card {
		padding: 0.875rem 1rem;
		border-radius: var(--radius-sm, 8px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		transition: border-color 0.15s ease;
	}

	.fact-card:hover {
		border-color: var(--color-border-hover, var(--color-accent-border));
	}

	.fact-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
	}

	.fact-content {
		font-size: 0.8125rem;
		color: var(--color-text-primary);
		font-weight: 500;
		line-height: 1.4;
	}

	.delete-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 0.25rem;
		border-radius: var(--radius-sm, 4px);
		background: transparent;
		border: none;
		color: var(--color-text-muted);
		cursor: pointer;
		transition: color 0.15s ease, background 0.15s ease;
	}

	.delete-btn:hover {
		color: var(--color-error);
		background: var(--color-error-tint);
	}

	/* Playbooks */
	.playbooks-list {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.playbook-card {
		padding: 1rem;
		border-radius: var(--radius, 10px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.playbook-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
	}

	.playbook-title {
		font-weight: 600;
		font-size: 0.875rem;
		color: var(--color-text-primary);
	}

	.playbook-body {
		padding: 0.75rem 0.875rem;
		border-radius: var(--radius-sm, 6px);
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
		font-size: 0.8125rem;
	}

	.symptom-row, .diag-row, .action-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.label {
		color: var(--color-text-muted);
		font-size: 0.75rem;
		min-width: 5rem;
	}

	.code-pill {
		font-family: var(--font-mono, monospace);
		font-size: 0.75rem;
		background: var(--color-bg-primary);
		padding: 2px 6px;
		border-radius: var(--radius-sm, 4px);
		border: 1px solid var(--color-border);
		color: var(--color-text-primary);
	}

	.action-pill {
		font-family: var(--font-mono, monospace);
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-accent);
		background: var(--color-accent-tint);
		padding: 2px 6px;
		border-radius: var(--radius-sm, 4px);
		border: 1px solid var(--color-accent-border);
	}

	.playbook-footer {
		display: flex;
		align-items: center;
		justify-content: space-between;
		font-size: 0.71875rem;
		color: var(--color-text-muted);
		padding-top: 0.25rem;
	}

	/* Journal */
	.journal-timeline {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.journal-entry {
		padding: 0.875rem 1rem;
		border-radius: var(--radius-sm, 8px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		font-size: 0.8125rem;
	}

	.journal-top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
	}

	.trigger-code {
		font-family: var(--font-mono, monospace);
		font-size: 0.75rem;
		color: var(--color-text-primary);
	}

	.journal-query {
		font-size: 0.78125rem;
		color: var(--color-text-secondary);
	}

	.journal-advice {
		padding: 0.625rem 0.75rem;
		border-radius: var(--radius-sm, 6px);
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		font-size: 0.75rem;
		line-height: 1.4;
	}

	.journal-action {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.75rem;
	}

	/* Sentinel Settings */
	.sentinel-settings-card {
		padding: 1.25rem;
		border-radius: var(--radius, 10px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		display: flex;
		flex-direction: column;
		gap: 1.25rem;
	}

	.toggle-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
	}

	.toggle-checkbox {
		width: 1.25rem;
		height: 1.25rem;
		cursor: pointer;
		accent-color: var(--color-accent);
	}

	.settings-divider {
		height: 1px;
		background: var(--color-border);
	}

	.autonomy-options {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.autonomy-card {
		display: flex;
		align-items: flex-start;
		gap: 0.875rem;
		padding: 0.875rem 1rem;
		border-radius: var(--radius-sm, 8px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
		cursor: pointer;
		transition: all 0.15s ease;
	}

	.autonomy-card:hover {
		background: var(--color-bg-hover);
		border-color: var(--color-border-hover, var(--color-accent-border));
	}

	.autonomy-card.selected {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}

	.autonomy-content {
		display: flex;
		flex-direction: column;
	}

	.interval-input {
		width: 6rem;
		height: 2.25rem;
		padding: 0 0.625rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-size: 0.8125rem;
		outline: none;
	}
</style>
