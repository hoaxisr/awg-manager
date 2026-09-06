import { DAY_WORDS, HOUR_WORDS, MINUTE_WORDS, pluralForm } from './pluralize';

/**
 * Format bytes to human readable string
 */
export function formatBytes(bytes: number, decimals = 2): string {
    if (bytes === 0) return '0 B';

    const k = 1024;
    const dm = decimals < 0 ? 0 : decimals;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];

    // Дробные значения < 1 (например, скорость 0.5 Б/с) дают log < 0 и
    // sizes[-1] === undefined; сверху ограничиваем последним юнитом.
    const i = Math.min(Math.max(Math.floor(Math.log(bytes) / Math.log(k)), 0), sizes.length - 1);

    return `${parseFloat((bytes / Math.pow(k, i)).toFixed(dm))} ${sizes[i]}`;
}

/**
 * Скорость в байтах/с с ФИКСИРОВАННЫМ одним знаком после запятой — ширина
 * строки не прыгает между тиками живых обновлений (та же причина, что у
 * formatTrafficStable в liveConnectionsStore).
 */
export function formatByteRate(bytesPerSec: number): string {
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    if (bytesPerSec <= 0) return `0.0 ${sizes[0]}/с`;
    const i = Math.min(Math.max(Math.floor(Math.log(bytesPerSec) / Math.log(k)), 0), sizes.length - 1);
    return `${(bytesPerSec / Math.pow(k, i)).toFixed(1)} ${sizes[i]}/с`;
}

/**
 * Format bytes/sec to human readable bits/sec string (Кбит/с, Мбит/с, Гбит/с)
 */
export function formatBitRate(bytesPerSec: number): string {
    const bits = bytesPerSec * 8;
    if (bits === 0) return '0 бит/с';

    const k = 1000;
    const sizes = ['бит/с', 'Кбит/с', 'Мбит/с', 'Гбит/с'];

    const i = Math.floor(Math.log(bits) / Math.log(k));
    const idx = Math.min(i, sizes.length - 1);

    return `${parseFloat((bits / Math.pow(k, idx)).toFixed(1))} ${sizes[idx]}`;
}

/**
 * Format duration in seconds to human readable string
 */
export function formatDuration(seconds: number): string {
    if (seconds < 60) return `${seconds} сек`;
    if (seconds < 3600) {
        const m = Math.floor(seconds / 60);
        return `${m} мин`;
    }
    const hours = Math.floor(seconds / 3600);
    if (hours < 24) {
        const m = Math.floor((seconds % 3600) / 60);
        return `${hours} ч ${m} мин`;
    }
    const days = Math.floor(hours / 24);
    const h = hours % 24;
    return `${days} д ${h} ч`;
}

/**
 * Calculate seconds elapsed since an ISO timestamp
 */
export function secondsSince(isoTimestamp: string): number {
    const d = new Date(isoTimestamp);
    if (isNaN(d.getTime())) return 0;
    return Math.max(0, Math.floor((Date.now() - d.getTime()) / 1000));
}

/**
 * Format timestamp to time only (HH:MM:SS)
 */
export function formatTime(timestamp: string): string {
    const date = new Date(timestamp);
    return date.toLocaleTimeString('ru-RU', {
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit'
    });
}

/**
 * Format timestamp to date + time (DD.MM HH:MM:SS)
 */
export function formatDate(timestamp: string): string {
    const date = new Date(timestamp);
    if (isNaN(date.getTime())) return '—';
    return date.toLocaleDateString('ru-RU', {
        day: '2-digit',
        month: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit'
    });
}

/**
 * Format timestamp using explicit UTC offset minutes (router timezone),
 * independent from browser locale timezone.
 * Output: YYYY-MM-DD HH:mm:ss
 */
export function formatDateTimeWithOffset(timestamp: string, offsetMinutes?: number): string {
    const d = new Date(timestamp);
    if (isNaN(d.getTime())) return timestamp;

    const pad = (n: number) => String(n).padStart(2, '0');
    const formatOffset = (mins: number): string => {
        const sign = mins >= 0 ? '+' : '-';
        const abs = Math.abs(mins);
        const hours = Math.floor(abs / 60);
        const minutes = abs % 60;
        return `${sign}${pad(hours)}:${pad(minutes)}`;
    };

    // Fallback for missing offset: use UTC-formatted value (not browser local timezone).
    if (offsetMinutes === undefined || offsetMinutes === null || !Number.isFinite(offsetMinutes)) {
        return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())}+00:00`;
    }

    const shiftedMs = d.getTime() + offsetMinutes * 60_000;
    const shifted = new Date(shiftedMs);

    // Use UTC getters after applying offset shift to avoid browser timezone effects.
    return `${shifted.getUTCFullYear()}-${pad(shifted.getUTCMonth() + 1)}-${pad(shifted.getUTCDate())} ${pad(shifted.getUTCHours())}:${pad(shifted.getUTCMinutes())}:${pad(shifted.getUTCSeconds())}${formatOffset(offsetMinutes)}`;
}

/**
 * Format relative time in Russian (e.g., "2 минуты назад")
 */
export function formatRelativeTime(timestamp: string | Date): string {
    const date = typeof timestamp === 'string' ? new Date(timestamp) : timestamp;

    // Handle invalid dates
    if (isNaN(date.getTime())) return '—';

    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffSec = Math.floor(diffMs / 1000);

    if (diffSec < 0) return 'только что';
    if (diffSec < 10) return 'только что';
    if (diffSec < 60) return `${diffSec} сек. назад`;

    const diffMin = Math.floor(diffSec / 60);
    if (diffMin < 60) {
        return `${diffMin} ${pluralForm(diffMin, MINUTE_WORDS)} назад`;
    }

    const diffHours = Math.floor(diffSec / 3600);
    if (diffHours < 24) {
        return `${diffHours} ${pluralForm(diffHours, HOUR_WORDS)} назад`;
    }

    const diffDays = Math.floor(diffSec / 86400);
    return `${diffDays} ${pluralForm(diffDays, DAY_WORDS)} назад`;
}
