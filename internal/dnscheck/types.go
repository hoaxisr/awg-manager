package dnscheck

type CheckResult struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Title   string `json:"title"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

type StartResponse struct {
	ClientIP string        `json:"clientIP"`
	Hostname string        `json:"hostname"`
	Checks   []CheckResult `json:"checks"`
}
