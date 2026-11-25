<!--
  Карточка «Доступ»: вход по паролю роутера, время жизни сессии и HTTP-сервер
  панели. Сохранение общих настроек — у страницы (ontoggleAuth,
  onsaveSessionTtl); черновик времени сессии — здесь.
-->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Toggle } from '$lib/components/ui';
	import { Lock } from 'lucide-svelte';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import HttpServerCard from './HttpServerCard.svelte';
	import {
		clampSessionTtlHours,
		SESSION_TTL_MIN_HOURS,
		SESSION_TTL_MAX_HOURS,
	} from './sessionTtl';
	import { pageDraft } from './pageState.svelte';

	interface Props {
		authEnabled: boolean;
		/** Сохранённое значение; старые бэкенды поле не отдают → 24 ч. */
		sessionTtlHours: number | undefined;
		saving?: boolean;
		ontoggleAuth: (enabled: boolean) => void;
		onsaveSessionTtl: (hours: number) => void;
	}

	let { authEnabled, sessionTtlHours, saving = false, ontoggleAuth, onsaveSessionTtl }: Props = $props();

	const ttl = pageDraft<number | null>('session-ttl', () => clampSessionTtlHours(sessionTtlHours));

	function saveSessionTtl() {
		const hours = clampSessionTtlHours(ttl.value);
		ttl.value = hours;
		onsaveSessionTtl(hours);
	}
</script>

<div class="settings-block">
	<div class="card">
	<SettingsSectionLabel label={m.settings_page_access_title()} icon={Lock} tone="blue" header />
	<div class="setting-row toggle-inline-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">{m.settings_page_auth_label()}</span>
			<span class="setting-description">
				{m.settings_page_auth_description()}
			</span>
		</div>
		<Toggle checked={authEnabled} controlled onchange={ontoggleAuth} disabled={saving} />
	</div>
	{#if authEnabled}
		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">{m.settings_page_session_ttl_label()}</span>
				<span class="setting-description">
					{m.settings_page_session_ttl_description()}
				</span>
			</div>
			<div class="inline-form">
				<div class="input-with-suffix">
					<input
						type="number"
						id="sessionTtlHours"
						bind:value={ttl.value}
						min={SESSION_TTL_MIN_HOURS}
						max={SESSION_TTL_MAX_HOURS}
						disabled={saving}
					/>
					<span class="input-suffix">{m.settings_page_hours_suffix()}</span>
				</div>
				{#if ttl.dirty}
					<Button variant="primary" size="sm" onclick={saveSessionTtl} loading={saving}>
						{saving ? m.common_saving() : m.common_save()}
					</Button>
				{/if}
			</div>
		</div>
	{/if}
	<HttpServerCard />
	</div>
</div>
