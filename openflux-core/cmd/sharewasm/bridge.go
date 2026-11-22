package main

import (
	"encoding/json"
	"strings"

	"github.com/p1neappleXpress/OpenFlux/share"
)

func marshalWasmError(msg string) string {
	b, _ := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: msg})
	return string(b)
}

func readLinkJSON(link string) string {
	return share.Read(link).JSON()
}

func makeLinkJSON(configJSON string) string {
	return share.MakeJSON(configJSON).JSON()
}

func qrLinkJSON(link string) string {
	if strings.TrimSpace(link) == "" {
		return marshalWasmError("empty link")
	}
	bm, err := share.Bitmap(link)
	if err != nil {
		return marshalWasmError(err.Error())
	}
	rows := make([]string, len(bm))
	for y, row := range bm {
		buf := make([]byte, len(row))
		for x, dark := range row {
			if dark {
				buf[x] = '1'
			} else {
				buf[x] = '0'
			}
		}
		rows[y] = string(buf)
	}
	b, err := json.Marshal(struct {
		Rows []string `json:"rows"`
	}{Rows: rows})
	if err != nil {
		return marshalWasmError("marshal qr rows: " + err.Error())
	}
	return string(b)
}
