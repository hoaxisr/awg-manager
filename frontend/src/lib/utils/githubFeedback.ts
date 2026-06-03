export const GITHUB_NEW_ISSUE_URL = 'https://github.com/hoaxisr/awg-manager/issues/new';

// Bug-report issue FORM (.github/ISSUE_TEMPLATE/bug_report.yml on the default
// branch). Forms ignore the ?body= param, so the incident flow opens this URL
// bare and relies on the form's own fields (the structured text is still copied
// to the clipboard for the user to paste in).
export const GITHUB_BUG_REPORT_URL = `${GITHUB_NEW_ISSUE_URL}?template=bug_report.yml`;

export function buildGitHubIssueUrl(title: string, body: string): string {
	const url = new URL(GITHUB_NEW_ISSUE_URL);
	url.searchParams.set('title', title);
	url.searchParams.set('body', body);
	return url.toString();
}

export function buildSuggestionIssueUrl(): string {
	const body = [
		'## Что хотите сообщить',
		'',
		'<!-- Опишите идею, предложение или вопрос -->',
		'',
		'## Важно',
		'',
		'AWG Manager — open-source проект без службы поддержки и SLA. Ответ не гарантируется.',
	].join('\n');
	return buildGitHubIssueUrl('Сообщение или предложение AWG Manager', body);
}
