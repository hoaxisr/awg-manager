package installer

// InstallState классифицирует состояние managed sing-box-бинаря относительно
// pin-версии и доступного дискового места. UI использует это, чтобы
// корректно отрендерить баннер (особенно "no space" варианты, в которых
// мы намеренно НЕ пытаемся скачивать).
type InstallState string

const (
	// InstallStateInstalled — бинарь на диске, версия и SHA совпадают с pinned
	// (или это UPX-сжатая копия pinned-версии, см. Installer.MatchesPinnedBytes).
	InstallStateInstalled InstallState = "installed"
	// InstallStateMissing — gate пропускает: clean install ИЛИ outdated-binary
	// с достаточным местом. UI различает install vs update через отдельный
	// updateAvailable-флаг.
	InstallStateMissing InstallState = "missing"
	// InstallStateMissingNoSpace — бинарь не установлен, и места под него не хватает.
	InstallStateMissingNoSpace InstallState = "missing_no_space"
	// InstallStateOutdatedNoSpace — установлен старый бинарь (версия и/или SHA отличаются, не UPX-копия pinned),
	// но места под новый не хватает. Старый продолжает работать.
	InstallStateOutdatedNoSpace InstallState = "outdated_no_space"
	// InstallStateInstalling — идёт активная установка/апгрейд (выставляется по сигналу
	// от installProgress; для status-poll'а — резервный вариант).
	InstallStateInstalling InstallState = "installing"
	// InstallStateError — последняя установка/апгрейд завершились ошибкой.
	InstallStateError InstallState = "error"
)
