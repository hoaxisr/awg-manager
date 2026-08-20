// Helper to classify file types and assign nice icon colors / hints
export type FileTypeInfo = {
	kind: 'folder' | 'config' | 'script' | 'log' | 'archive' | 'binary' | 'cert' | 'db' | 'text' | 'generic';
	color: string;
	badge?: string;
};

export function getFileTypeInfo(name: string, isDir: boolean): FileTypeInfo {
	if (isDir) {
		return { kind: 'folder', color: '#60a5fa' };
	}

	const lower = name.toLowerCase();

	if (lower.endsWith('.json') || lower.endsWith('.yaml') || lower.endsWith('.yml') || lower.endsWith('.conf') || lower.endsWith('.ini') || lower.endsWith('.toml')) {
		return { kind: 'config', color: '#f59e0b', badge: 'CONFIG' };
	}

	if (lower.endsWith('.sh') || lower.endsWith('.bash') || lower.endsWith('.py') || lower.startsWith('s') && /^[sS]\d\d/.test(lower)) {
		return { kind: 'script', color: '#10b981', badge: 'SCRIPT' };
	}

	if (lower.endsWith('.log') || lower.includes('.log.')) {
		return { kind: 'log', color: '#a855f7', badge: 'LOG' };
	}

	if (lower.endsWith('.tar') || lower.endsWith('.gz') || lower.endsWith('.tgz') || lower.endsWith('.zip') || lower.endsWith('.ipk') || lower.endsWith('.apk')) {
		return { kind: 'archive', color: '#ec4899', badge: 'ARCHIVE' };
	}

	if (lower.endsWith('.db') || lower.endsWith('.sqlite') || lower.endsWith('.sqlite3')) {
		return { kind: 'db', color: '#06b6d4', badge: 'DB' };
	}

	if (lower.endsWith('.crt') || lower.endsWith('.key') || lower.endsWith('.pem') || lower.endsWith('.pub')) {
		return { kind: 'cert', color: '#eab308', badge: 'KEY' };
	}

	if (lower.endsWith('.txt') || lower.endsWith('.md')) {
		return { kind: 'text', color: '#94a3b8' };
	}

	return { kind: 'generic', color: '#94a3b8' };
}
