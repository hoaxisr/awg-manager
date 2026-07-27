/**
 * Импорт .conf в новый AWG-туннель: drag-and-drop поверх ghost-терминала,
 * выбор файла, выбор backend'а (nativewg/kernel) и переход в редактор.
 *
 * Вынесено из routes/+page.svelte при расщеплении навигации v3: тем же срезом
 * пользуется и страница /awg/tunnels, и ghost-терминал дашборда главной
 * (AwgTunnelsTabSection рендерится на обеих поверхностях). Код перенесён
 * дословно, системная информация приходит геттером.
 */
import { goto } from '$app/navigation';
import { tunnels } from '$lib/stores/tunnels';
import { notifications } from '$lib/stores/notifications';
import { nativewgUnavailableHint } from '$lib/utils/backendAvailability';
import type { SystemInfo } from '$lib/types';

export interface AwgConfigImport {
	readonly dragOver: boolean;
	readonly importing: boolean;
	readonly nativewgHint: string;
	selectedBackend: 'nativewg' | 'kernel';
	fileInput: HTMLInputElement | undefined;
	handleDrop(event: DragEvent): void;
	handleDragOver(event: DragEvent): void;
	handleDragLeave(): void;
	handleFileSelect(event: Event): void;
}

export function createAwgConfigImport(getSysInfo: () => SystemInfo | null): AwgConfigImport {
	let dragOver = $state(false);
	let importing = $state(false);
	let selectedBackend = $state<'nativewg' | 'kernel'>('nativewg');
	let fileInput = $state<HTMLInputElement>();

	const nativewgHint = $derived.by(() => {
		const sysInfo = getSysInfo();
		return sysInfo !== null && !sysInfo.backendAvailability?.nativewg
			? nativewgUnavailableHint(sysInfo.nativewgReason)
			: '';
	});

	// Auto-select backend based on availability
	$effect(() => {
		const sysInfo = getSysInfo();
		if (sysInfo?.backendAvailability && !sysInfo.backendAvailability.nativewg && sysInfo.backendAvailability.kernel) {
			selectedBackend = 'kernel';
		}
	});

	function readAndImport(file: File) {
		const reader = new FileReader();
		reader.onload = async (e) => {
			const content = e.target?.result as string;
			if (!content?.trim()) return;
			importing = true;
			try {
				const name = file.name.replace(/\.conf$/i, '');
				const tunnel = await tunnels.importConfig(content, name, selectedBackend);
				if (tunnel.warnings?.length) {
					tunnel.warnings.forEach((w) => notifications.warning(w));
				}
				notifications.success('Туннель импортирован');
				goto(`/awg/tunnels/${tunnel.id}`);
			} catch (err) {
				notifications.error(err instanceof Error ? err.message : 'Ошибка импорта');
			} finally {
				importing = false;
			}
		};
		reader.readAsText(file);
	}

	return {
		get dragOver() { return dragOver; },
		get importing() { return importing; },
		get nativewgHint() { return nativewgHint; },
		get selectedBackend() { return selectedBackend; },
		set selectedBackend(v) { selectedBackend = v; },
		get fileInput() { return fileInput; },
		set fileInput(v) { fileInput = v; },
		handleDrop(event: DragEvent) {
			event.preventDefault();
			dragOver = false;
			if (event.dataTransfer?.files?.[0]) {
				readAndImport(event.dataTransfer.files[0]);
			}
		},
		handleDragOver(event: DragEvent) {
			event.preventDefault();
			dragOver = true;
		},
		handleDragLeave() {
			dragOver = false;
		},
		handleFileSelect(event: Event) {
			const input = event.target as HTMLInputElement;
			if (input.files?.[0]) {
				readAndImport(input.files[0]);
			}
		},
	};
}
