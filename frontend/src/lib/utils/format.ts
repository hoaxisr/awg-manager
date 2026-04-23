/**
 * Format bytes to human readable string
 */
export function formatBytes(bytes: number, decimals = 2): string {
    if (bytes === 0) return '0 B';

    const k = 1024;
    const dm = decimals < 0 ? 0 : decimals;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];

    const i = Math.floor(Math.log(bytes) / Math.log(k));

    return `${parseFloat((bytes / Math.pow(k, i)).toFixed(dm))} ${sizes[i]}`;
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
    return date.toLocaleDateString('ru-RU', {
        day: '2-digit',
        month: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit'
    });
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
        const lastDigit = diffMin % 10;
        const lastTwo = diffMin % 100;
        let word = 'минут';
        if (lastTwo >= 11 && lastTwo <= 19) word = 'минут';
        else if (lastDigit === 1) word = 'минуту';
        else if (lastDigit >= 2 && lastDigit <= 4) word = 'минуты';
        return `${diffMin} ${word} назад`;
    }

    const diffHours = Math.floor(diffSec / 3600);
    if (diffHours < 24) {
        const lastDigit = diffHours % 10;
        const lastTwo = diffHours % 100;
        let word = 'часов';
        if (lastTwo >= 11 && lastTwo <= 19) word = 'часов';
        else if (lastDigit === 1) word = 'час';
        else if (lastDigit >= 2 && lastDigit <= 4) word = 'часа';
        return `${diffHours} ${word} назад`;
    }

    const diffDays = Math.floor(diffSec / 86400);
    const lastDigit = diffDays % 10;
    const lastTwo = diffDays % 100;
    let word = 'дней';
    if (lastTwo >= 11 && lastTwo <= 19) word = 'дней';
    else if (lastDigit === 1) word = 'день';
    else if (lastDigit >= 2 && lastDigit <= 4) word = 'дня';
    return `${diffDays} ${word} назад`;
}
