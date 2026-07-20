package awg3endpoint

import (
	"encoding/json"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

type TagInfo struct {
	Tag  string `json:"tag"`
	Kind string `json:"kind"` // "awg3"
}

// Валидирующая запись: SaveAndValidate прогоняет sing-box check и на невалидном
// конфиге НЕ применяет его (см. orchestrator/draft.go). Sync возвращает ошибку,
// чтобы handler откатил только что добавленную запись.
type Orchestrator interface {
	SaveAndValidate(slot orchestrator.Slot, jsonBytes []byte) (orchestrator.ValidationResult, error)
}

type Service struct {
	store *Store // тот же пакет awg3endpoint (Task 2)
	orch  Orchestrator
}

func NewService(store *Store, orch Orchestrator) *Service {
	return &Service{store: store, orch: orch}
}

// Sync материализует store → 16-awg3.json как {"endpoints":[...]}, перезаписывая
// поле tag каждого endpoint на человекочитаемый Record.Tag.
func (s *Service) Sync() error {
	list, err := s.store.List()
	if err != nil {
		return err
	}
	eps := make([]json.RawMessage, 0, len(list))
	for _, rec := range list {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(rec.Endpoint, &obj); err != nil {
			continue // битую запись пропускаем (не роняем весь slot)
		}
		tagJSON, _ := json.Marshal(rec.Tag)
		obj["tag"] = tagJSON
		merged, err := json.Marshal(obj)
		if err != nil {
			continue
		}
		eps = append(eps, merged)
	}
	data, err := json.MarshalIndent(map[string]any{"endpoints": eps}, "", "  ")
	if err != nil {
		return err
	}
	res, err := s.orch.SaveAndValidate(orchestrator.SlotAwg3, data)
	if err != nil {
		return err
	}
	// ВАЖНО (ревью I-1): при провале валидации SaveAndValidate возвращает (res, nil) —
	// err==nil. ValidationResult имеет Ok() bool и Error() string (НЕ Valid/Message).
	if !res.Ok() {
		return fmt.Errorf("sing-box check: %s", res.Error())
	}
	return nil
}

func (s *Service) ListTags() []TagInfo {
	list, _ := s.store.List()
	out := make([]TagInfo, 0, len(list))
	for _, rec := range list {
		out = append(out, TagInfo{Tag: rec.Tag, Kind: "awg3"})
	}
	return out
}
