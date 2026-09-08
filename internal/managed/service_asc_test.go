package managed

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSetASCParams_RejectsEmptyPayload(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address:    "10.31.0.1",
		Mask:       "255.255.255.0",
		ListenPort: 52031,
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	if err := svc.SetASCParams(context.Background(), server.InterfaceName, json.RawMessage(`{}`)); err == nil {
		t.Fatalf("expected validation error for empty ASC payload")
	}
}

func TestSetASCParams_RejectsEmptyRequiredFields(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address:    "10.32.0.1",
		Mask:       "255.255.255.0",
		ListenPort: 52032,
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	raw := json.RawMessage(`{
		"jc":3,"jmin":64,"jmax":256,"s1":15,"s2":16,
		"h1":"","h2":"","h3":"","h4":""
	}`)
	err = svc.SetASCParams(context.Background(), server.InterfaceName, raw)
	if err == nil {
		t.Fatalf("expected validation error for empty required ASC text field")
	}
	for _, k := range []string{"h1", "h2", "h3", "h4"} {
		if !strings.Contains(err.Error(), k) {
			t.Fatalf("expected aggregate error to mention %s, got: %v", k, err)
		}
	}
}

func TestSetASCParams_AllowsEmptySignatureFields(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address:    "10.33.0.1",
		Mask:       "255.255.255.0",
		ListenPort: 52033,
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	raw := json.RawMessage(`{
		"jc":3,"jmin":64,"jmax":256,"s1":15,"s2":16,
		"h1":"100000001","h2":"1200000002","h3":"2400000003","h4":"3600000004",
		"i1":"","i2":"","i3":"","i4":"","i5":""
	}`)
	if err := svc.SetASCParams(context.Background(), server.InterfaceName, raw); err != nil {
		t.Fatalf("SetASCParams must accept empty i1..i5: %v", err)
	}
}

func TestSetASCParams_ExtendedPairValidation(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address:    "10.34.0.1",
		Mask:       "255.255.255.0",
		ListenPort: 52034,
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	base := `"jc":3,"jmin":64,"jmax":256,"s1":15,"s2":16,"h1":"100000001","h2":"1200000002","h3":"2400000003","h4":"3600000004"`

	if err := svc.SetASCParams(context.Background(), server.InterfaceName, json.RawMessage(`{`+base+`,"s3":null,"s4":10}`)); err == nil {
		t.Fatalf("expected error when s3 is null in extended payload")
	}
	if err := svc.SetASCParams(context.Background(), server.InterfaceName, json.RawMessage(`{`+base+`,"s3":8}`)); err == nil {
		t.Fatalf("expected error when s4 is missing in extended payload")
	}
	if err := svc.SetASCParams(context.Background(), server.InterfaceName, json.RawMessage(`{`+base+`,"s3":8,"s4":10}`)); err != nil {
		t.Fatalf("expected valid extended payload to pass: %v", err)
	}
}

func TestSetASCParams_AllowsZeroDisabledState(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address:    "10.35.0.1",
		Mask:       "255.255.255.0",
		ListenPort: 52035,
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	raw := json.RawMessage(`{
		"jc":0,"jmin":0,"jmax":0,"s1":0,"s2":0,
		"h1":"","h2":"","h3":"","h4":""
	}`)
	if err := svc.SetASCParams(context.Background(), server.InterfaceName, raw); err != nil {
		t.Fatalf("zero/default disabled ASC payload must be accepted: %v", err)
	}
}

func TestSetASCParams_AllowsExtendedDisabledStateWithEmptyS3S4(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address:    "10.42.0.1",
		Mask:       "255.255.255.0",
		ListenPort: 52042,
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	raw := json.RawMessage(`{
		"jc":0,"jmin":0,"jmax":0,"s1":0,"s2":0,
		"h1":"","h2":"","h3":"","h4":"",
		"s3":"","s4":"",
		"i1":"","i2":"","i3":"","i4":"","i5":""
	}`)
	if err := svc.SetASCParams(context.Background(), server.InterfaceName, raw); err != nil {
		t.Fatalf("extended disabled ASC payload with empty s3/s4 must be accepted: %v", err)
	}
}

func TestSetASCParams_RejectsPartialDisabledState(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address:    "10.37.0.1",
		Mask:       "255.255.255.0",
		ListenPort: 52037,
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	raw := json.RawMessage(`{
		"jc":0,"jmin":0,"jmax":0,"s1":0,"s2":0,
		"h1":"100","h2":"","h3":"","h4":""
	}`)
	if err := svc.SetASCParams(context.Background(), server.InterfaceName, raw); err == nil {
		t.Fatalf("expected validation error for partial disabled ASC state")
	}
}

func TestSetASCParams_RejectsMultipleInvalidNumericFields(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address:    "10.38.0.1",
		Mask:       "255.255.255.0",
		ListenPort: 52038,
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	raw := json.RawMessage(`{
		"jc":0,"jmin":0,"jmax":0,"s1":0,"s2":0,
		"h1":"100","h2":"","h3":"","h4":""
	}`)
	err = svc.SetASCParams(context.Background(), server.InterfaceName, raw)
	if err == nil {
		t.Fatalf("expected validation error")
	}
	for _, k := range []string{"jc", "jmin", "jmax", "s1", "s2"} {
		if !strings.Contains(err.Error(), k) {
			t.Fatalf("expected aggregate error to mention %s, got: %v", k, err)
		}
	}
}

func TestSetASCParams_RejectsJmaxNotGreaterThanJmin(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address:    "10.36.0.1",
		Mask:       "255.255.255.0",
		ListenPort: 52036,
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	raw := json.RawMessage(`{
		"jc":3,"jmin":128,"jmax":128,"s1":15,"s2":16,
		"h1":"100000001","h2":"1200000002","h3":"2400000003","h4":"3600000004"
	}`)
	if err := svc.SetASCParams(context.Background(), server.InterfaceName, raw); err == nil {
		t.Fatalf("expected validation error when jmax <= jmin")
	}
}

func TestSetASCParams_AllowsZeroDisabledState_UsesClearPath(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address:    "10.39.0.1",
		Mask:       "255.255.255.0",
		ListenPort: 52039,
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	raw := json.RawMessage(`{"jc":0,"jmin":0,"jmax":0,"s1":0,"s2":0,"h1":"","h2":"","h3":"","h4":""}`)
	if err := svc.SetASCParams(context.Background(), server.InterfaceName, raw); err != nil {
		t.Fatalf("SetASCParams: %v", err)
	}

	poster := svc.transport.(*recordingPoster)
	foundClear := false
	for _, post := range poster.posts {
		iface, ok := post["interface"].(map[string]interface{})
		if !ok {
			continue
		}
		row, ok := iface[server.InterfaceName].(map[string]interface{})
		if !ok {
			continue
		}
		wg, ok := row["wireguard"].(map[string]interface{})
		if !ok {
			continue
		}
		asc, ok := wg["asc"].(map[string]interface{})
		if !ok {
			continue
		}
		if no, ok := asc["no"].(bool); ok && no {
			foundClear = true
		}
	}
	if !foundClear {
		t.Fatalf("expected clear ASC payload with no=true")
	}
}

func TestSetASCParams_DisabledStateFailsWhenReadBackStillEnabled(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address:    "10.43.0.1",
		Mask:       "255.255.255.0",
		ListenPort: 52043,
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	poster := svc.transport.(*recordingPoster)
	originalOnPost := poster.onPost
	poster.onPost = func(_ map[string]interface{}) {
		// Simulate router ignoring ASC clear.
	}
	defer func() { poster.onPost = originalOnPost }()

	raw := json.RawMessage(`{
		"jc":0,"jmin":0,"jmax":0,"s1":0,"s2":0,
		"h1":"","h2":"","h3":"","h4":""
	}`)

	if err := svc.SetASCParams(context.Background(), server.InterfaceName, raw); err == nil {
		t.Fatalf("expected read-back mismatch error when disabled ASC clear is ignored")
	}
}

func TestSetASCParams_SetFailsWhenReadBackDiffers(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address:    "10.40.0.1",
		Mask:       "255.255.255.0",
		ListenPort: 52040,
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	poster := svc.transport.(*recordingPoster)
	originalOnPost := poster.onPost
	poster.onPost = func(_ map[string]interface{}) {
		// Simulate router ignoring ASC updates.
	}
	defer func() { poster.onPost = originalOnPost }()

	raw := json.RawMessage(`{
		"jc":3,"jmin":64,"jmax":256,"s1":15,"s2":16,
		"h1":"100000001","h2":"1200000002","h3":"2400000003","h4":"3600000004"
	}`)
	if err := svc.SetASCParams(context.Background(), server.InterfaceName, raw); err == nil {
		t.Fatalf("expected read-back mismatch error")
	}
}

// Сигнатура принадлежит пиру: i1..i5 в теле сервера отбрасываются перед
// NDMS и больше нигде не сохраняются (замена теста про персист I1-I5).
func TestSetASCParams_StripsSignatureAndPersistsNothing(t *testing.T) {
	svc, store, _ := newCreateTestService(t)
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address:    "10.41.0.1",
		Mask:       "255.255.255.0",
		ListenPort: 52041,
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	raw := json.RawMessage(`{
		"jc":3,"jmin":64,"jmax":256,"s1":15,"s2":16,
		"h1":"100000001","h2":"1200000002","h3":"2400000003","h4":"3600000004",
		"i1":"sig1","i2":"sig2","i3":"sig3","i4":"sig4","i5":"sig5"
	}`)
	if err := svc.SetASCParams(context.Background(), server.InterfaceName, raw); err != nil {
		t.Fatalf("SetASCParams: %v", err)
	}

	poster := svc.transport.(*recordingPoster)
	for _, post := range poster.posts {
		iface, ok := post["interface"].(map[string]interface{})
		if !ok {
			continue
		}
		row, ok := iface[server.InterfaceName].(map[string]interface{})
		if !ok {
			continue
		}
		wg, ok := row["wireguard"].(map[string]interface{})
		if !ok {
			continue
		}
		asc, ok := wg["asc"].(map[string]interface{})
		if !ok {
			continue
		}
		if _, hasI1 := asc["i1"]; hasI1 {
			t.Fatalf("i1 must be stripped before NDMS ASC payload: %+v", asc)
		}
	}

	got, ok := store.GetManagedServerByID(server.InterfaceName)
	if !ok {
		t.Fatalf("server missing in store")
	}
	if got.LegacyI1 != "" || got.LegacyI2 != "" || got.LegacyI3 != "" || got.LegacyI4 != "" || got.LegacyI5 != "" {
		t.Fatalf("server must not store a signature, got: %+v", got)
	}
}
