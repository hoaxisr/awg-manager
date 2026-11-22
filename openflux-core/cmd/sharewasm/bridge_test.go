package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestShareWasmBridge_StreamRoundTripAndQR(t *testing.T) {
	cfgJSON := `{"name":"phpbox · mailru","mode":"stream","transports":[{"type":"mailru","url":"https://cloud.mail.ru/public/AAA/111"}]}`
	made := makeLinkJSON(cfgJSON)

	var makeRes struct {
		Link  string `json:"link"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(made), &makeRes); err != nil {
		t.Fatalf("unmarshal makeLinkJSON: %v (%s)", err, made)
	}
	if makeRes.Error != "" || !strings.HasPrefix(makeRes.Link, "openflux://v1/") {
		t.Fatalf("unexpected makeLinkJSON result: %+v", makeRes)
	}

	read := readLinkJSON(makeRes.Link)
	var readRes struct {
		Config *struct {
			Name       string `json:"name"`
			Mode       string `json:"mode"`
			Transports []struct {
				Type string `json:"type"`
				URL  string `json:"url"`
			} `json:"transports"`
		} `json:"config"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(read), &readRes); err != nil {
		t.Fatalf("unmarshal readLinkJSON: %v (%s)", err, read)
	}
	if readRes.Error != "" || readRes.Config == nil || readRes.Config.Mode != "stream" {
		t.Fatalf("unexpected readLinkJSON result: %+v", readRes)
	}
	if len(readRes.Config.Transports) != 1 || readRes.Config.Transports[0].URL != "https://cloud.mail.ru/public/AAA/111" {
		t.Fatalf("unexpected transports in readLinkJSON: %+v", readRes.Config.Transports)
	}

	qrOut := qrLinkJSON(makeRes.Link)
	var qrRes struct {
		Rows  []string `json:"rows"`
		Error string   `json:"error"`
	}
	if err := json.Unmarshal([]byte(qrOut), &qrRes); err != nil {
		t.Fatalf("unmarshal qrLinkJSON: %v (%s)", err, qrOut)
	}
	if qrRes.Error != "" || len(qrRes.Rows) < 21 {
		t.Fatalf("unexpected qrLinkJSON output: err=%q rows=%d", qrRes.Error, len(qrRes.Rows))
	}
	n := len(qrRes.Rows)
	for _, row := range qrRes.Rows {
		if len(row) != n {
			t.Fatalf("non-square QR row: len=%d want=%d", len(row), n)
		}
		if strings.Trim(row, "01") != "" {
			t.Fatalf("unexpected character in QR bitmap row: %q", row)
		}
	}
}

func TestShareWasmBridge_EncryptedAndErrors(t *testing.T) {
	encJSON := `{"name":"SessionNode","negotiate":true,"secret":"0123456789abcdef","transports":[{"type":"direct","dial":"203.0.113.10:8445"}]}`
	made := makeLinkJSON(encJSON)
	var makeRes struct {
		Link    string `json:"link"`
		Context string `json:"context"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal([]byte(made), &makeRes); err != nil {
		t.Fatal(err)
	}
	if makeRes.Error != "" || makeRes.Link == "" {
		t.Fatalf("encrypted makeLinkJSON failed: %+v", makeRes)
	}

	read := readLinkJSON(makeRes.Link)
	if strings.Contains(read, `"error"`) {
		t.Fatalf("encrypted readLinkJSON returned error: %s", read)
	}

	bad := readLinkJSON("https://not-an-openflux-link")
	var badRes struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(bad), &badRes); err != nil {
		t.Fatal(err)
	}
	if badRes.Error == "" || badRes.Code == "" {
		t.Fatalf("expected structured error/code for malformed link, got %s", bad)
	}

	if emptyQR := qrLinkJSON("   "); !strings.Contains(emptyQR, `"error"`) {
		t.Fatalf("expected error JSON for empty qrLinkJSON, got %s", emptyQR)
	}
}
