package wdttdiag

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
)

const (
	diagLinePrefix     = "DIAG|"
	maxDiagOutputBytes = 512 * 1024
)

type rawDiagEvent struct {
	Ev      string         `json:"ev"`
	Mode    string         `json:"mode"`
	Step    string         `json:"step"`
	Title   string         `json:"title"`
	State   string         `json:"state"`
	Text    string         `json:"text"`
	Code    string         `json:"code"`
	Sev     string         `json:"sev"`
	Actions []string       `json:"actions"`
	Facts   map[string]any `json:"facts"`
}

// parseDiagOutput разбирает строки "DIAG|{json}" из stdout `wt-client -diag`:
//   - start  -> режим диагностики (`mode`)
//   - step   -> дедупликация по `step` ID с сохранением порядка первого появления
//   - verdict -> итоговый вердикт
func parseDiagOutput(stdout string) (mode string, steps []Step, verdict *Verdict) {
	steps = []Step{}
	idxByID := make(map[string]int)

	for _, rawLine := range strings.Split(stdout, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if !strings.HasPrefix(line, diagLinePrefix) {
			continue
		}
		payload := strings.TrimPrefix(line, diagLinePrefix)
		var ev rawDiagEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		switch ev.Ev {
		case "start":
			if m := strings.TrimSpace(ev.Mode); m != "" {
				mode = m
			}
		case "step":
			if ev.Step == "" {
				continue
			}
			if idx, exists := idxByID[ev.Step]; exists {
				title := ev.Title
				if title == "" {
					title = steps[idx].Title
				}
				steps[idx] = Step{
					Step:  ev.Step,
					Title: title,
					State: ev.State,
					Text:  ev.Text,
				}
			} else {
				idxByID[ev.Step] = len(steps)
				steps = append(steps, Step{
					Step:  ev.Step,
					Title: ev.Title,
					State: ev.State,
					Text:  ev.Text,
				})
			}
		case "verdict":
			v := Verdict{
				Code:  ev.Code,
				Sev:   ev.Sev,
				Title: ev.Title,
				Text:  ev.Text,
			}
			if len(ev.Actions) > 0 {
				v.Actions = append([]string(nil), ev.Actions...)
			}
			if len(ev.Facts) > 0 {
				v.Facts = ev.Facts
			}
			verdict = &v
		}
	}
	return mode, steps, verdict
}

// redactSecrets маскирует секреты длиной >= 3 в диагностическом выводе.
func redactSecrets(text string, secrets ...string) string {
	out := text
	seen := make(map[string]struct{}, len(secrets)*2)
	for _, raw := range secrets {
		s := strings.TrimSpace(raw)
		if len(s) < 3 {
			continue
		}
		for _, candidate := range []string{s, url.QueryEscape(s)} {
			if len(candidate) < 3 {
				continue
			}
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			out = strings.ReplaceAll(out, candidate, "[REDACTED]")
		}
	}
	return out
}

// isUnsupportedFlagErr распознаёт отказ старого `wt-client`, не знающего флагов диагностики.
func isUnsupportedFlagErr(output string) bool {
	if !strings.Contains(output, "flag provided but not defined:") {
		return false
	}
	for _, flag := range []string{"-diag", "-diag-key", "-turn-transport"} {
		if strings.Contains(output, flag) {
			return true
		}
	}
	return false
}

// limitedBuffer ограничивает объём вывода дочернего процесса в памяти роутера,
// не возвращая ошибку записи (иначе дочерний процесс получит short write / EPIPE).
type limitedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	origLen := len(p)
	if b.buf.Len() < maxDiagOutputBytes {
		rem := maxDiagOutputBytes - b.buf.Len()
		if len(p) > rem {
			p = p[:rem]
			b.truncated = true
		}
		_, _ = b.buf.Write(p)
	} else if len(p) > 0 {
		b.truncated = true
	}
	return origLen, nil
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}
