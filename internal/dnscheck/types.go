package dnscheck

import "time"

type CheckResult struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Title   string `json:"title"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

type StartResponse struct {
	Token    string        `json:"token"`
	ClientIP string        `json:"clientIP"`
	Hostname string        `json:"hostname"`
	Port     int           `json:"port"`
	Checks   []CheckResult `json:"checks"`
}

type CompleteRequest struct {
	Token      string `json:"token"`
	DNSReached bool   `json:"dnsReached"`
}

type CompleteResponse struct {
	Checks []CheckResult `json:"checks"`
}

type tokenState struct {
	token     string
	clientIP  string
	hostname  string
	domain    string
	routerIP  string
	checks    []CheckResult
	reached   bool
	createdAt time.Time
}
