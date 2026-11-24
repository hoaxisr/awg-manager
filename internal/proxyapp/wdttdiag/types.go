package wdttdiag

// Request — опциональное тело POST /api/proxyrt/instances/{key}/diag.
type Request struct {
	Transport string `json:"transport,omitempty" enums:",auto,udp,tcp"`
	DiagKey   string `json:"diagKey,omitempty"`
}

// Step — шаг диагностики из события DIAG|{"ev":"step",...}.
type Step struct {
	Step  string `json:"step"`
	Title string `json:"title"`
	State string `json:"state"`
	Text  string `json:"text,omitempty"`
}

// Verdict — итоговый вердикт диагностики из события DIAG|{"ev":"verdict",...}.
type Verdict struct {
	Code    string         `json:"code"`
	Sev     string         `json:"sev"`
	Title   string         `json:"title"`
	Text    string         `json:"text"`
	Actions []string       `json:"actions,omitempty"`
	Facts   map[string]any `json:"facts,omitempty" swaggertype:"object"`
}

// Result — полезная нагрузка ответа POST /api/proxyrt/instances/{key}/diag.
type Result struct {
	Mode       string   `json:"mode,omitempty"`
	Transport  string   `json:"transport,omitempty"`
	DurationMs int64    `json:"durationMs"`
	ExitCode   int      `json:"exitCode"`
	Steps      []Step   `json:"steps"`
	Verdict    *Verdict `json:"verdict,omitempty"`
}

// DiagResponse — конверт ответа ручки диагностики для OpenAPI/Swagger.
type DiagResponse struct {
	Success bool   `json:"success" example:"true"`
	Data    Result `json:"data"`
}
