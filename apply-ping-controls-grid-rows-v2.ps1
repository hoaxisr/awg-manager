$ErrorActionPreference = "Stop"

$path = "frontend/src/routes/settings/+page.svelte"
$text = Get-Content -Raw -Encoding UTF8 $path

function Replace-FirstRegex {
	param(
		[string]$Text,
		[string]$Pattern,
		[string]$Replacement,
		[string]$Label
	)
	$rx = [regex]::new($Pattern, [System.Text.RegularExpressions.RegexOptions]::Singleline -bor [System.Text.RegularExpressions.RegexOptions]::Multiline)
	$m = $rx.Match($Text)
	if (-not $m.Success) {
		throw "$Label not found"
	}
	return $Text.Substring(0, $m.Index) + $Replacement + $Text.Substring($m.Index + $m.Length)
}

$text = Replace-FirstRegex `
	-Text $text `
	-Pattern '^\s*\.ping-target-controls\s*\{.*?^\s*\}\s*\r?\n' `
	-Replacement @'
	.ping-target-controls {
		display: grid;
		grid-template-columns: minmax(8rem, 0.78fr) minmax(16rem, 1.22fr) 7.5rem;
		grid-template-rows: auto 32px;
		gap: 0.25rem 0.625rem;
		width: 100%;
		min-width: 0;
		align-items: stretch;
	}

'@ `
	-Label 'desktop .ping-target-controls'

$text = Replace-FirstRegex `
	-Text $text `
	-Pattern '^\s*\.ping-target-field\s*\{.*?^\s*\}\s*\r?\n' `
	-Replacement @'
	.ping-target-field {
		display: contents;
		min-width: 0;
		color: var(--color-text-secondary);
		font-size: 0.75rem;
		font-weight: 600;
	}

	.ping-target-field:nth-child(1) > span,
	.ping-target-field:nth-child(1) > input {
		grid-column: 1;
	}

	.ping-target-field:nth-child(2) > span,
	.ping-target-field:nth-child(2) > input {
		grid-column: 2;
	}

	.ping-target-field > span {
		grid-row: 1;
		align-self: end;
	}

	.ping-target-field > input {
		grid-row: 2;
		min-width: 0;
	}

'@ `
	-Label 'desktop .ping-target-field'

# Remove the old small helper block if it still exists; the display:contents grid-row block owns input placement now.
$text = [regex]::Replace(
	$text,
	'(?ms)^\s*\.ping-target-field input\s*\{.*?^\s*\}\s*\r?\n',
	'',
	1
)

$text = Replace-FirstRegex `
	-Text $text `
	-Pattern '^\s*\.ping-target-action\s*\{.*?^\s*\}\s*\r?\n' `
	-Replacement @'
	.ping-target-action {
		grid-column: 3;
		grid-row: 2;
		display: flex;
		align-items: stretch;
		justify-content: stretch;
		align-self: stretch;
		min-width: 0;
	}

'@ `
	-Label 'desktop .ping-target-action'

$text = Replace-FirstRegex `
	-Text $text `
	-Pattern '^\s*\.ping-target-action\s+:global\(\.btn\)\s*\{.*?^\s*\}\s*\r?\n' `
	-Replacement @'
	.ping-target-action :global(.btn) {
		width: 100%;
		min-width: 7.5rem;
		height: 32px;
		min-height: 32px;
		max-height: 32px;
		box-sizing: border-box;
		padding-block: 0;
	}

'@ `
	-Label 'desktop .ping-target-action button'

# Remove previous copy of this override if script is re-run.
$text = [regex]::Replace(
	$text,
	'(?ms)\n\t/\* ping target mobile grid-row reset \(scripted\) \*/\s*@media \(max-width: 640px\) \{.*?\n\t\}\s*\n(?=\n\t@media \(max-width: 900px\))',
	"`n",
	1
)

$mobileOverride = @'
	/* ping target mobile grid-row reset (scripted) */
	@media (max-width: 640px) {
		.ping-target-controls {
			grid-template-columns: minmax(0, 1fr);
			grid-template-rows: auto;
			gap: 0.5rem;
		}

		.ping-target-field {
			display: grid;
			gap: 0.25rem;
		}

		.ping-target-field:nth-child(1) > span,
		.ping-target-field:nth-child(1) > input,
		.ping-target-field:nth-child(2) > span,
		.ping-target-field:nth-child(2) > input,
		.ping-target-field > span,
		.ping-target-field > input {
			grid-column: auto;
			grid-row: auto;
		}

		.ping-target-action {
			grid-column: auto;
			grid-row: auto;
			align-self: stretch;
			justify-content: stretch;
		}
	}

'@

$marker = "`t@media (max-width: 900px) {"
if (-not $text.Contains($marker)) {
	throw "Cannot find @media (max-width: 900px) insertion marker"
}
$text = $text.Replace($marker, $mobileOverride + $marker)

Set-Content -Encoding UTF8 -NoNewline $path $text
Write-Host "updated $path"
