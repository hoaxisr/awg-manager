package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

type ASCValidationIssue struct {
	Field   string
	Message string
}

type ASCValidationError struct {
	Issues []ASCValidationIssue
}

func (e ASCValidationError) Error() string {
	if len(e.Issues) == 0 {
		return "invalid ASC parameters"
	}
	parts := make([]string, 0, len(e.Issues))
	for _, i := range e.Issues {
		parts = append(parts, fmt.Sprintf("%s %s", i.Field, i.Message))
	}
	return "invalid ASC parameters: " + strings.Join(parts, "; ")
}

func (e *ASCValidationError) add(field, message string) {
	e.Issues = append(e.Issues, ASCValidationIssue{Field: field, Message: message})
}

func extractASCSignatures(raw json.RawMessage) (i1, i2, i3, i4, i5 string, _ error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", "", "", "", "", err
	}
	get := func(key string) (string, error) {
		v, ok := obj[key]
		if !ok || isASCRequiredValueEmpty(v) {
			return "", nil
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return "", fmt.Errorf("asc param %s must be a string", key)
		}
		return s, nil
	}
	var out [5]string
	for i, key := range []string{"i1", "i2", "i3", "i4", "i5"} {
		s, err := get(key)
		if err != nil {
			return "", "", "", "", "", err
		}
		out[i] = s
	}
	return out[0], out[1], out[2], out[3], out[4], nil
}

func stripASCSignatures(raw json.RawMessage) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("parse ASC params: %w", err)
	}
	delete(obj, "i1")
	delete(obj, "i2")
	delete(obj, "i3")
	delete(obj, "i4")
	delete(obj, "i5")
	return marshalNoEscape(obj)
}

func isASCRequiredValueEmpty(v json.RawMessage) bool {
	trimmed := bytes.TrimSpace(v)
	if len(trimmed) == 0 {
		return true
	}
	if bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) {
		return true
	}
	return false
}

func getASCPositiveNumber(obj map[string]json.RawMessage, key string) (float64, error) {
	v, ok := obj[key]
	if !ok || isASCRequiredValueEmpty(v) {
		return 0, fmt.Errorf("ASC parameter %s is required", key)
	}

	var n float64
	if err := json.Unmarshal(v, &n); err != nil {
		return 0, fmt.Errorf("ASC parameter %s must be a number", key)
	}
	if n <= 0 {
		return 0, fmt.Errorf("ASC parameter %s must be greater than zero", key)
	}
	return n, nil
}

func isASCDisabledState(obj map[string]json.RawMessage) bool {
	requiredZero := []string{"jc", "jmin", "jmax", "s1", "s2"}
	for _, key := range requiredZero {
		v, ok := obj[key]
		if !ok {
			return false
		}
		var n float64
		if err := json.Unmarshal(v, &n); err != nil || n != 0 {
			return false
		}
	}

	for _, key := range []string{"h1", "h2", "h3", "h4"} {
		v, ok := obj[key]
		if !ok {
			return false
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil || strings.TrimSpace(s) != "" {
			return false
		}
	}

	for _, key := range []string{"s3", "s4"} {
		v, ok := obj[key]
		if !ok {
			continue
		}
		if isASCRequiredValueEmpty(v) {
			continue
		}
		var n float64
		if err := json.Unmarshal(v, &n); err != nil || n != 0 {
			return false
		}
	}

	return true
}

func validateASCParamsRequired(raw json.RawMessage) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("parse ASC params: %w", err)
	}
	if len(obj) == 0 {
		return ASCValidationError{Issues: []ASCValidationIssue{
			{Field: "asc", Message: "payload is empty"},
		}}
	}

	// i1..i5 нестрокового типа extractASCSignatures глотал в "" — сигнатура
	// исчезала молча. null и "" допустимы: так NDMS отдаёт пустое поле.
	for _, k := range []string{"i1", "i2", "i3", "i4", "i5"} {
		v, ok := obj[k]
		if !ok || isASCRequiredValueEmpty(v) {
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return fmt.Errorf("asc param %s must be a string", k)
		}
	}

	if isASCDisabledState(obj) {
		return nil
	}

	var verr ASCValidationError
	numFields := []string{"jc", "jmin", "jmax", "s1", "s2"}
	nums := map[string]float64{}
	for _, key := range numFields {
		n, err := getASCPositiveNumber(obj, key)
		if err != nil {
			if strings.Contains(err.Error(), "required") {
				verr.add(key, "is required")
			} else if strings.Contains(err.Error(), "must be a number") {
				verr.add(key, "must be a number")
			} else {
				verr.add(key, "must be greater than zero")
			}
			continue
		}
		nums[key] = n
	}
	if len(verr.Issues) == 0 || (nums["jmin"] > 0 && nums["jmax"] > 0) {
		if nums["jmax"] <= nums["jmin"] {
			verr.add("jmax", "must be greater than jmin")
		}
	}

	requiredText := []string{"h1", "h2", "h3", "h4"}
	for _, key := range requiredText {
		v, ok := obj[key]
		if !ok || isASCRequiredValueEmpty(v) {
			verr.add(key, "is required")
			continue
		}
		var text string
		if err := json.Unmarshal(v, &text); err != nil || strings.TrimSpace(text) == "" {
			verr.add(key, "is required")
		}
	}

	_, hasS3 := obj["s3"]
	_, hasS4 := obj["s4"]
	if hasS3 || hasS4 {
		if _, err := getASCPositiveNumber(obj, "s3"); err != nil {
			if strings.Contains(err.Error(), "required") {
				verr.add("s3", "is required")
			} else if strings.Contains(err.Error(), "must be a number") {
				verr.add("s3", "must be a number")
			} else {
				verr.add("s3", "must be greater than zero")
			}
		}
		if _, err := getASCPositiveNumber(obj, "s4"); err != nil {
			if strings.Contains(err.Error(), "required") {
				verr.add("s4", "is required")
			} else if strings.Contains(err.Error(), "must be a number") {
				verr.add("s4", "must be a number")
			} else {
				verr.add("s4", "must be greater than zero")
			}
		}
	}

	if len(verr.Issues) > 0 {
		return verr
	}
	return nil
}

func (s *Service) generateDefaultASCParams() (json.RawMessage, error) {
	extended := osdetect.AtLeast(5, 1)
	hRanges := ndmsinfo.SupportsHRanges()
	return generateASCParamsRaw(extended, hRanges, rand.NewSource(time.Now().UnixNano()))
}

func (s *Service) applyASCParams(ctx context.Context, iface query.Confirmed, raw json.RawMessage) error {
	ifaceName := iface.Name()
	if err := validateASCParamsRequired(raw); err != nil {
		return err
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("parse ASC params: %w", err)
	}

	if isASCDisabledState(obj) {
		if err := s.rciClearASCParams(ctx, iface); err != nil {
			return fmt.Errorf("clear ASC params: %w", err)
		}
		if err := s.verifyASCParamsApplied(ctx, ifaceName, raw); err != nil {
			return err
		}
		return nil
	}

	stripped, err := stripASCSignatures(raw)
	if err != nil {
		return err
	}
	// Форма managed шлёт только поля 2.0, а запись без 3.x их снимает (5.02.A.11).
	if s.queries == nil || s.queries.WGServers == nil {
		return fmt.Errorf("cannot read ASC params: WGServers query store is not initialized")
	}
	current, err := s.queries.WGServers.ASC3Fields(ctx, ifaceName)
	if err != nil {
		return err
	}
	if stripped, err = ndms.KeepASC3(stripped, current); err != nil {
		return err
	}
	if err := s.rciSetASCParams(ctx, iface, stripped); err != nil {
		return fmt.Errorf("set ASC params: %w", err)
	}
	if err := s.verifyASCParamsApplied(ctx, ifaceName, raw); err != nil {
		return err
	}
	return nil
}

// ValidateASCParams exposes managed ASC validation for callers that need to
// decide whether ASC payload is exportable/applicable.
func ValidateASCParams(raw json.RawMessage) error {
	return validateASCParamsRequired(raw)
}

func normalizeASCRaw(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	normalized := map[string]json.RawMessage{}
	for k, v := range obj {
		switch k {
		case "i1", "i2", "i3", "i4", "i5":
			continue
		default:
			normalized[k] = v
		}
	}
	return normalized, nil
}

func normalizeASCForCompare(raw json.RawMessage) (map[string]string, bool, error) {
	obj, err := normalizeASCRaw(raw)
	if err != nil {
		return nil, false, err
	}
	disabled := isASCDisabledState(obj)
	out := map[string]string{}
	for k, v := range obj {
		trimmed := bytes.TrimSpace(v)
		switch k {
		case "h1", "h2", "h3", "h4":
			var s string
			if err := json.Unmarshal(trimmed, &s); err != nil {
				return nil, false, err
			}
			out[k] = strings.TrimSpace(s)
		case "jc", "jmin", "jmax", "s1", "s2", "s3", "s4":
			if isASCRequiredValueEmpty(trimmed) {
				out[k] = "0"
				continue
			}
			var n float64
			if err := json.Unmarshal(trimmed, &n); err != nil {
				return nil, false, err
			}
			out[k] = fmt.Sprintf("%g", n)
		}
	}
	return out, disabled, nil
}

func isDisabledReadback(actual map[string]string) bool {
	for _, k := range []string{"jc", "jmin", "jmax", "s1", "s2"} {
		if actual[k] != "0" && actual[k] != "" {
			return false
		}
	}
	for _, k := range []string{"h1", "h2", "h3", "h4"} {
		if strings.TrimSpace(actual[k]) != "" {
			return false
		}
	}
	for _, k := range []string{"s3", "s4"} {
		if actual[k] != "" && actual[k] != "0" {
			return false
		}
	}
	return true
}

func (s *Service) verifyASCParamsApplied(ctx context.Context, ifaceName string, requested json.RawMessage) error {
	want, wantDisabled, err := normalizeASCForCompare(requested)
	if err != nil {
		return fmt.Errorf("normalize requested ASC: %w", err)
	}
	if s.queries == nil || s.queries.WGServers == nil {
		return fmt.Errorf("cannot verify ASC params: WGServers query store is not initialized")
	}

	extended := osdetect.AtLeast(5, 1)
	if !extended {
		if _, ok := want["s3"]; ok {
			extended = true
		}
		if _, ok := want["s4"]; ok {
			extended = true
		}
	}
	// Сверка — одно свежее дерево rc (F581, F600): rc отражает запись сразу
	// после ответа на POST, несовпадение — сразу «не применено». Второе
	// дерево — только после ошибки чтения первого (R44).
	var actual map[string]string
	var readErr error
	for attempt := range 2 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("%w: %v", ErrASCUnverified, ctx.Err())
			case <-time.After(ascVerifyPause):
			}
		}
		if actual, readErr = s.readASCFresh(ctx, ifaceName, extended); readErr == nil {
			break
		}
	}
	if readErr != nil {
		return fmt.Errorf("%w: %v", ErrASCUnverified, readErr)
	}
	if !ascReadbackMatches(want, wantDisabled, actual) {
		return fmt.Errorf("ASC params were not applied by router: want=%v got=%v", want, actual)
	}
	return nil
}

// ErrASCUnverified — запись ASC роутер принял (POST без ошибки), а прочитать
// результат для сверки не удалось. Это не отказ записи: Create не откатывает
// сервер, restore не падает, правка публикует изменение (R44).
var ErrASCUnverified = errors.New("роутер принял запись ASC, но прочитать результат для проверки не удалось")

// ascUnverified — err несёт ErrASCUnverified: шаг не провален (R44), в журнал
// уходит предупреждение. Прочие ошибки — false, решает вызывающий.
func (s *Service) ascUnverified(op, ifaceName string, err error) bool {
	if !errors.Is(err, ErrASCUnverified) {
		return false
	}
	s.appLog.Warn(op, ifaceName, err.Error())
	return true
}

// ascVerifyPause — пауза перед повторным чтением сверки после ОШИБКИ чтения
// первого. По несовпадению повтора нет: стенд 01.10 (5.01.C.6) — POST
// interface WireguardN wireguard asc {…} и сразу GET /rci/show/rc/interface/
// дали новые значения 20 из 20 под открытой панелью и 10 из 10 на буте (R44,
// F600).
const ascVerifyPause = 300 * time.Millisecond

// readASCFresh — ASC интерфейса из дерева rc, прочитанного сейчас, в форме
// для сравнения.
func (s *Service) readASCFresh(ctx context.Context, ifaceName string, extended bool) (map[string]string, error) {
	got, err := s.queries.WGServers.ASCParamsFresh(ctx, ifaceName, extended)
	if err != nil {
		return nil, err
	}
	actual, _, err := normalizeASCForCompare(got)
	return actual, err
}

func ascReadbackMatches(want map[string]string, wantDisabled bool, actual map[string]string) bool {
	if wantDisabled {
		return isDisabledReadback(actual)
	}
	for k, v := range want {
		if actual[k] != v {
			return false
		}
	}
	return true
}
