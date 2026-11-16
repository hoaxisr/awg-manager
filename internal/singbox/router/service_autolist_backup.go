package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// autolistBackupFile — имя файла снимка удалённых объектов Autolist в
// каталоге данных панели (рядом с settings.json).
const autolistBackupFile = "autolist-removed-backup.json"

// ErrAutolistBackupNotFound — файла снимка нет (или он пуст): восстанавливать
// нечего, отвергнуть «начать заново» нельзя.
var ErrAutolistBackupNotFound = errors.New("снимок удалённых объектов Autolist не найден")

// ErrAutolistBackupExists — все три объекта Autolist уже присутствуют:
// восстановление не требуется (сателлит успел пересоздать их сам).
var ErrAutolistBackupExists = errors.New("объекты Autolist уже существуют, восстановление не требуется")

// AutolistBackup — снимок удалённых объектов в форме, из которой Restore
// пересоздаёт их через тот же Add* набор, что и ручное редактирование.
// Rule/RuleSet/Outbound — DTO конфига (не view): восстановление идёт в слот
// router, чей источник снимка — ListRules/ListRuleSets/ListCompositeOutbounds.
type AutolistBackup struct {
	Version    int       `json:"version"`
	CapturedAt time.Time `json:"capturedAt"`
	// Rule — owner-правило (nil: объекта не было).
	Rule *Rule `json:"rule,omitempty"`
	// RuleSet — inline-набор awgm-autolist (nil: объекта не было).
	RuleSet *RuleSet `json:"ruleSet,omitempty"`
	// Outbound — direct awgm-autolist-target (nil: объекта не было).
	Outbound *Outbound `json:"outbound,omitempty"`
	// TargetInterface/TargetOutbound — цель, известная панели на момент
	// сноса (последний статус сателлита). Информационные: writer сателлита
	// восстанавливает mapping при следующей записи сам.
	TargetInterface string `json:"targetInterface,omitempty"`
	TargetOutbound  string `json:"targetOutbound,omitempty"`
}

// autolistBackupPath — путь файла снимка: каталог данных панели из настроек
// (та же база, что у settings.json). Deps.Settings обязателен в бою; при nil
// (тесты минимального сервиса) операции со снимком отказывают.
func (s *ServiceImpl) autolistBackupPath() (string, error) {
	if s.deps.Settings == nil {
		return "", fmt.Errorf("каталог данных недоступен (settings store не подключён)")
	}
	return filepath.Join(s.deps.Settings.DataDir(), autolistBackupFile), nil
}

// loadAutolistBackup читает снимок. ErrAutolistBackupNotFound — файла нет.
func loadAutolistBackup(path string) (*AutolistBackup, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrAutolistBackupNotFound
		}
		return nil, fmt.Errorf("прочитать снимок: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("снимок повреждён: файл пуст")
	}
	var snap AutolistBackup
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("снимок повреждён: %w", err)
	}
	if snap.Version != 1 {
		return nil, fmt.Errorf("снимок повреждён: неизвестная версия %d", snap.Version)
	}
	return &snap, nil
}

// writeAutolistBackup атомарно записывает снимок (temp+rename, как у соседних
// файлов каталога данных, internal/storage/atomic.go).
func writeAutolistBackup(path string, snap *AutolistBackup) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	data = append(data, '\n')
	if err := storage.AtomicWritePerm(path, data, storage.SecretFilePermission); err != nil {
		return fmt.Errorf("записать снимок: %w", err)
	}
	return nil
}

// CaptureAutolistSnapshot сохраняет объекты Autolist (owner-правило, rule-set,
// outbound) в файл снимка ПЕРЕД их удалением кнопкой сноса. Все объекты
// отсутствуют — файл снимка убирается (cleanup): предлагать восстановление
// нечего. Вызывается из TeardownAutolistObjects после чтения списков, до
// первого Delete; ошибка снимка teardown НЕ отменяет — ответ обработчика
// несёт backupSaved, чтобы фронт показал предупреждение.
func (s *ServiceImpl) CaptureAutolistSnapshot(ctx context.Context) error {
	path, err := s.autolistBackupPath()
	if err != nil {
		return err
	}
	rules, err := s.ListRules(ctx)
	if err != nil {
		return err
	}
	sets, err := s.ListRuleSets(ctx)
	if err != nil {
		return err
	}
	outbounds, err := s.ListCompositeOutbounds(ctx)
	if err != nil {
		return err
	}

	snap := AutolistBackup{Version: 1, CapturedAt: time.Now().UTC()}
	for _, r := range rules {
		if isAutolistOwnerRule(r) {
			owner := r
			snap.Rule = &owner
			break
		}
		// Чужие правила, ссылающиеся на outbound, снос отклонил бы — в снимок
		// они не попадают: восстановление пересоздаёт только собственные
		// объекты.
	}
	for i, rs := range sets {
		if rs.Tag == autolistRuleSetTag {
			set := stripRuleSetProjection(sets[i])
			snap.RuleSet = &set
			break
		}
	}
	for _, ob := range outbounds {
		if ob.Tag != autolistTargetOutbound {
			continue
		}
		own := ob.Outbound
		snap.Outbound = &own
		break
	}
	// TargetInterface/TargetOutbound из снимка сознательно опущены: сателлит —
	// внешний демон, панель не хранит его последнюю цель, а writer
	// восстанавливает mapping при следующей записи сам.
	if snap.Rule == nil && snap.RuleSet == nil && snap.Outbound == nil {
		// Нечего сохранять: убираем устаревший снимок, если он есть.
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("убрать устаревший снимок: %w", rmErr)
		}
		return nil
	}
	return writeAutolistBackup(path, &snap)
}

// stripRuleSetProjection вычищает память-only поля проекции списка перед
// снимком: снимок хранит то, что Add* примет обратно.
func stripRuleSetProjection(rs RuleSet) RuleSet {
	rs.MaterializedSRS = false
	rs.fetchVia = ""
	rs.restoreFailed = false
	return rs
}

// RestoreAutolistObjects пересоздаёт объекты Autolist из файла снимка тем же
// draft→apply набором, что и ручное редактирование. Идемпотентен по
// частичному состоянию: восстанавливает только недостающие объекты; все три
// на месте — отказ «восстановление не требуется». bind_interface из снимка
// проверяется каталогом bindable — иначе прямая привязка к исчезнувшему
// интерфейсу не подняла бы туннель. При успехе файл снимка удаляется:
// восстановлено, больше не предлагать.
func (s *ServiceImpl) RestoreAutolistObjects(ctx context.Context) error {
	path, err := s.autolistBackupPath()
	if err != nil {
		return err
	}
	snap, err := loadAutolistBackup(path)
	if err != nil {
		return err
	}

	rules, err := s.ListRules(ctx)
	if err != nil {
		return err
	}
	sets, err := s.ListRuleSets(ctx)
	if err != nil {
		return err
	}
	outbounds, err := s.ListCompositeOutbounds(ctx)
	if err != nil {
		return err
	}
	haveRule, haveSet, haveOutbound := false, false, false
	for _, r := range rules {
		if isAutolistOwnerRule(r) {
			haveRule = true
		}
	}
	for _, rs := range sets {
		if rs.Tag == autolistRuleSetTag {
			haveSet = true
		}
	}
	for _, ob := range outbounds {
		if ob.Tag == autolistTargetOutbound {
			haveOutbound = true
		}
	}
	if haveRule && haveSet && haveOutbound {
		return ErrAutolistBackupExists
	}
	// Частичное существование: восстанавливаемое правило/набор ссылаются
	// друг на друга и на outbound, так что при частичном состоянии объект,
	// которого «нет», и есть тот, что восстановим. Обратная ситуация — в
	// конфиге есть owner-правило/набор из снимка, но в СНИМКЕ этого объекта
	// нет — значит, сателлит пересоздал его сам: восстановление не нужно.
	if haveRule || haveSet || haveOutbound {
		if (haveRule && snap.Rule == nil) || (haveSet && snap.RuleSet == nil) || (haveOutbound && snap.Outbound == nil) {
			return ErrAutolistBackupExists
		}
	}

	// bind_interface снимка обязан оставаться привязываемым: интерфейс мог
	// исчезнуть (VPN выключен) после сноса. Проверка ДО любых мутаций.
	if snap.Outbound != nil && !haveOutbound && snap.Outbound.BindInterface != "" {
		if err := s.validateBindInterface(ctx, snap.Outbound.BindInterface); err != nil {
			return fmt.Errorf("интерфейс %q из снимка больше недоступен: %w", snap.Outbound.BindInterface, err)
		}
	}

	if !haveOutbound && snap.Outbound != nil {
		if err := s.AddCompositeOutbound(ctx, *snap.Outbound); err != nil {
			return err
		}
	}
	if !haveSet && snap.RuleSet != nil {
		if err := s.AddRuleSet(ctx, stripRuleSetProjection(*snap.RuleSet)); err != nil {
			return err
		}
	}
	if !haveRule && snap.Rule != nil {
		if err := s.AddRule(ctx, *snap.Rule); err != nil {
			return err
		}
	}
	res, err := s.ApplyStaging(ctx)
	if err != nil && !errors.Is(err, orchestrator.ErrNoDraft) {
		return err
	}
	if err == nil && !res.Ok() {
		return fmt.Errorf("восстановление отклонено валидацией применяемого конфига: %s", res.Error())
	}
	if err := s.verifyAutolistRestored(ctx, snap); err != nil {
		return err
	}
	if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
		return fmt.Errorf("убрать использованный снимок: %w", rmErr)
	}
	return nil
}

// verifyAutolistRestored доказывает по прочитанному состоянию, что все
// объекты снимка присутствуют после apply — то же доказательство readback,
// что у teardown и writer'а сателлита.
func (s *ServiceImpl) verifyAutolistRestored(ctx context.Context, snap *AutolistBackup) error {
	rules, err := s.ListRules(ctx)
	if err != nil {
		return err
	}
	if snap.Rule != nil {
		found := false
		for _, r := range rules {
			if isAutolistOwnerRule(r) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("readback: owner-правило не восстановлено")
		}
	}
	sets, err := s.ListRuleSets(ctx)
	if err != nil {
		return err
	}
	if snap.RuleSet != nil {
		found := false
		for _, rs := range sets {
			if rs.Tag == autolistRuleSetTag {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("readback: rule-set %q не восстановлен", autolistRuleSetTag)
		}
	}
	outbounds, err := s.ListCompositeOutbounds(ctx)
	if err != nil {
		return err
	}
	if snap.Outbound != nil {
		found := false
		for _, ob := range outbounds {
			if ob.Tag == autolistTargetOutbound {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("readback: outbound %q не восстановлен", autolistTargetOutbound)
		}
	}
	return nil
}

// AutolistBackupInfo сообщает, есть ли снимок и когда он снят (для GET-ручки
// и флага статуса hasRemovedBackup).
func (s *ServiceImpl) AutolistBackupInfo(ctx context.Context) (bool, time.Time, error) {
	_ = ctx // путь берётся из настроек; ctx оставлен для будущих источников
	path, err := s.autolistBackupPath()
	if err != nil {
		return false, time.Time{}, err
	}
	snap, err := loadAutolistBackup(path)
	if err != nil {
		if errors.Is(err, ErrAutolistBackupNotFound) {
			return false, time.Time{}, nil
		}
		return false, time.Time{}, err
	}
	return true, snap.CapturedAt, nil
}

// DeleteAutolistBackup убирает файл снимка — «начать заново»: пользователь
// отказался от восстановления. Отсутствующий файл — не ошибка.
func (s *ServiceImpl) DeleteAutolistBackup(ctx context.Context) error {
	_ = ctx
	path, err := s.autolistBackupPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("убрать снимок: %w", err)
	}
	return nil
}
