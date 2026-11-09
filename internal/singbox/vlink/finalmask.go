package vlink

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// parseFinalMask разбирает блок finalmask Xray — из streamSettings или из
// параметра fm= ссылки (тот же JSON, URL-кодированный) — и возвращает его
// для поля finalmask аутбаунда форка. Форк умеет одну маску, tcp fragment;
// остальные меняют формат на проводе, и узел без них подключился бы
// вхолостую, поэтому такой узел отвергается. quicParams и пустые списки
// формат не меняют и отбрасываются.
//
// Настройки fragment проверяются здесь теми же правилами, что в форке
// (option/finalmask.go): форк проверяет их при старте, и одна битая маска
// уронила бы всю конфигурацию, а не один узел.
func parseFinalMask(raw string) (json.RawMessage, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var final XrayFinalMask
	if err := json.Unmarshal([]byte(raw), &final); err != nil {
		return nil, fmt.Errorf("vlink: invalid finalmask")
	}
	unsupported := len(final.UDP)
	for _, m := range final.TCP {
		if !strings.EqualFold(m.Type, "fragment") {
			unsupported++
		}
	}
	if unsupported > 0 {
		return nil, fmt.Errorf("xray: finalmask has %d mask(s) with no sing-box equivalent", unsupported)
	}
	if len(final.TCP) == 0 {
		return nil, nil
	}
	tcp := make([]map[string]any, 0, len(final.TCP))
	for _, m := range final.TCP {
		settings := m.Settings
		if len(settings) == 0 || string(settings) == "null" {
			settings = json.RawMessage("{}")
		}
		if err := checkFragmentMask(settings); err != nil {
			return nil, fmt.Errorf("vlink: finalmask fragment: %w", err)
		}
		tcp = append(tcp, map[string]any{"type": "fragment", "settings": settings})
	}
	out, err := json.Marshal(map[string]any{"tcp": tcp})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func checkFragmentMask(settings json.RawMessage) error {
	var f struct {
		Packets  *string           `json:"packets"`
		Length   json.RawMessage   `json:"length"`
		Delay    json.RawMessage   `json:"delay"`
		Lengths  []json.RawMessage `json:"lengths"`
		Delays   []json.RawMessage `json:"delays"`
		MaxSplit json.RawMessage   `json:"maxSplit"`
	}
	if err := json.Unmarshal(settings, &f); err != nil {
		return fmt.Errorf("invalid settings")
	}
	if f.Packets != nil {
		switch p := strings.ToLower(*f.Packets); p {
		case "tlshello", "":
		default:
			from, _, err := parseMaskRange(json.RawMessage(strconv.Quote(p)))
			if err != nil {
				return fmt.Errorf("packets: %w", err)
			}
			if from == 0 {
				return fmt.Errorf("packets: from can't be 0")
			}
		}
	}
	lengths := f.Lengths
	if len(lengths) == 0 {
		lengths = []json.RawMessage{f.Length}
	}
	var lastFrom int64
	for _, r := range lengths {
		from, _, err := parseMaskRange(r)
		if err != nil {
			return fmt.Errorf("length: %w", err)
		}
		lastFrom = from
	}
	if lastFrom == 0 {
		return fmt.Errorf("last length min can't be 0")
	}
	for _, r := range append([]json.RawMessage{f.Delay, f.MaxSplit}, f.Delays...) {
		if _, _, err := parseMaskRange(r); err != nil {
			return err
		}
	}
	return nil
}

// parseMaskRange — диапазон в формах форка (common/xray/json/badoption.Range):
// число, строка "N" или "N-M", объект {from, to}. Отсутствие значения — 0.
// Отрицательные значения форк отвергает у всех полей fragment.
func parseMaskRange(raw json.RawMessage) (from, to int64, err error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, 0, nil
	}
	var n int64
	var s string
	var obj struct{ From, To int64 }
	switch {
	case json.Unmarshal(raw, &n) == nil:
		from, to = n, n
	case json.Unmarshal(raw, &s) == nil:
		parts := strings.Split(s, "-")
		if len(parts) > 2 {
			return 0, 0, fmt.Errorf("invalid range %q", s)
		}
		if from, err = strconv.ParseInt(parts[0], 10, 32); err != nil {
			return 0, 0, fmt.Errorf("invalid range %q", s)
		}
		to = from
		if len(parts) == 2 {
			if to, err = strconv.ParseInt(parts[1], 10, 32); err != nil {
				return 0, 0, fmt.Errorf("invalid range %q", s)
			}
		}
	case json.Unmarshal(raw, &obj) == nil:
		from, to = obj.From, obj.To
	default:
		return 0, 0, fmt.Errorf("invalid range %s", raw)
	}
	if from < 0 || from > to {
		return 0, 0, fmt.Errorf("invalid range %s", raw)
	}
	return from, to, nil
}
