package storage

import (
	"reflect"
	"strings"
	"testing"
)

type tHappyDst struct {
	A bool
	B string
	C int
	D struct {
		Field string
	}
}

type tHappyPatch struct {
	A *bool
	B *string
	C *int
	D *struct {
		Field string
	}
}

func ptrBool(v bool) *bool    { return &v }
func ptrStr(v string) *string { return &v }
func ptrInt(v int) *int       { return &v }

func TestApplyPatch_HappyPath_BoolFields(t *testing.T) {
	dst := tHappyDst{A: false}
	patch := tHappyPatch{A: ptrBool(true)}
	ApplyPatch(&dst, &patch)
	if dst.A != true {
		t.Errorf("A = %v, want true", dst.A)
	}
}

func TestApplyPatch_HappyPath_StringFields(t *testing.T) {
	dst := tHappyDst{B: "old"}
	patch := tHappyPatch{B: ptrStr("new")}
	ApplyPatch(&dst, &patch)
	if dst.B != "new" {
		t.Errorf("B = %q, want new", dst.B)
	}
}

func TestApplyPatch_HappyPath_StructValueField(t *testing.T) {
	dst := tHappyDst{}
	dst.D.Field = "old"
	patch := tHappyPatch{D: &struct{ Field string }{Field: "x"}}
	ApplyPatch(&dst, &patch)
	if dst.D.Field != "x" {
		t.Errorf("D.Field = %q, want x", dst.D.Field)
	}
}

func TestApplyPatch_NilPointerSkipped(t *testing.T) {
	dst := tHappyDst{A: false, B: "keep"}
	patch := tHappyPatch{A: ptrBool(true), B: nil}
	ApplyPatch(&dst, &patch)
	if dst.A != true {
		t.Errorf("A = %v, want true", dst.A)
	}
	if dst.B != "keep" {
		t.Errorf("B = %q, want keep (nil patch field must not overwrite)", dst.B)
	}
}

func TestApplyPatch_AppliesAllFieldsInOnePass(t *testing.T) {
	dst := tHappyDst{A: false, B: "old", C: 0}
	dst.D.Field = "old"
	patch := tHappyPatch{
		A: ptrBool(true),
		B: ptrStr("new"),
		C: ptrInt(42),
		D: &struct{ Field string }{Field: "newD"},
	}
	ApplyPatch(&dst, &patch)
	if dst.A != true || dst.B != "new" || dst.C != 42 || dst.D.Field != "newD" {
		t.Errorf("multi-field apply failed: %+v", dst)
	}
}

func TestApplyPatch_DownloadSettingsPatch_PartialKindPreservesTag(t *testing.T) {
	dst := Settings{
		Download: DownloadSettings{
			RouteTag:  "awg-1",
			RouteKind: "awg",
		},
	}
	kind := "singbox"
	patch := SettingsPatch{
		Download: &DownloadSettingsPatch{
			RouteKind: &kind,
		},
	}

	ApplyPatch(&dst, &patch)

	if dst.Download.RouteTag != "awg-1" {
		t.Fatalf("routeTag = %q, want awg-1", dst.Download.RouteTag)
	}
	if dst.Download.RouteKind != "singbox" {
		t.Fatalf("routeKind = %q, want singbox", dst.Download.RouteKind)
	}
}

type tSliceDst struct {
	Items []string
}

type tSlicePatch struct {
	Items *[]string
}

type tSubInner struct {
	X int
}

type tPtrDst struct {
	P *tSubInner
}

type tPtrPatch struct {
	P *tSubInner
}

type tBadPatchNonPointer struct {
	F bool // not a pointer — illegal
}

type tIncompatibleDst struct {
	F int
}

type tIncompatiblePatch struct {
	F *string
}

type tDstWithExtraField struct {
	A bool
	B string // not in patch
}

type tPatchWithExtraField struct {
	A *bool
	C *int // not in dst
}

type tDstUnexported struct {
	exportedA bool //nolint:unused
	A         bool
}

type tPatchUnexported struct {
	exportedA *bool //nolint:unused
	A         *bool
}

func ptrSlice(v []string) *[]string { return &v }

func TestApplyPatch_NilSrcReturnsCleanly(t *testing.T) {
	dst := tHappyDst{A: true, B: "x"}
	ApplyPatch[tHappyDst, tHappyPatch](&dst, nil)
	if dst.A != true || dst.B != "x" {
		t.Errorf("dst mutated by nil src: %+v", dst)
	}
}

func TestApplyPatch_NilDstPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil dst")
		}
		msg, _ := r.(string)
		if msg != "ApplyPatch: dst is nil" {
			t.Errorf("panic message = %q, want 'ApplyPatch: dst is nil'", msg)
		}
	}()
	patch := tHappyPatch{A: ptrBool(true)}
	ApplyPatch[tHappyDst, tHappyPatch](nil, &patch)
}

func TestApplyPatch_NonStructDstPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for non-struct dst")
		}
	}()
	var x int
	patch := tHappyPatch{A: ptrBool(true)}
	ApplyPatch(&x, &patch)
}

func TestApplyPatch_NonPointerFieldInSrcPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for non-pointer src field")
		}
		msg, _ := r.(string)
		if !containsSubstr(msg, "F") || !containsSubstr(msg, "not a pointer") {
			t.Errorf("panic message must mention field F and 'not a pointer', got: %s", msg)
		}
	}()
	dst := struct{ F bool }{}
	patch := tBadPatchNonPointer{F: true}
	ApplyPatch(&dst, &patch)
}

func TestApplyPatch_IncompatibleTypesPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for incompatible types")
		}
		msg, _ := r.(string)
		if !containsSubstr(msg, "F") || !containsSubstr(msg, "incompatible") {
			t.Errorf("panic message must mention field F and 'incompatible', got: %s", msg)
		}
	}()
	dst := tIncompatibleDst{F: 1}
	patch := tIncompatiblePatch{F: ptrStr("nope")}
	ApplyPatch(&dst, &patch)
}

func TestApplyPatch_SliceReplacedWholesale(t *testing.T) {
	t.Run("non-nil patch replaces", func(t *testing.T) {
		dst := tSliceDst{Items: []string{"x"}}
		patch := tSlicePatch{Items: ptrSlice([]string{"a", "b"})}
		ApplyPatch(&dst, &patch)
		if len(dst.Items) != 2 || dst.Items[0] != "a" || dst.Items[1] != "b" {
			t.Errorf("Items = %v, want [a b]", dst.Items)
		}
	})
	t.Run("explicit empty slice clears", func(t *testing.T) {
		dst := tSliceDst{Items: []string{"x"}}
		patch := tSlicePatch{Items: ptrSlice([]string{})}
		ApplyPatch(&dst, &patch)
		if len(dst.Items) != 0 {
			t.Errorf("Items = %v, want empty (explicit clearing)", dst.Items)
		}
	})
	t.Run("nil patch field preserves", func(t *testing.T) {
		dst := tSliceDst{Items: []string{"x"}}
		patch := tSlicePatch{Items: nil}
		ApplyPatch(&dst, &patch)
		if len(dst.Items) != 1 || dst.Items[0] != "x" {
			t.Errorf("Items = %v, want [x] (nil patch must preserve)", dst.Items)
		}
	})
}

func TestApplyPatch_PointerToPointerField(t *testing.T) {
	t.Run("non-nil patch assigns pointer", func(t *testing.T) {
		dst := tPtrDst{P: nil}
		patch := tPtrPatch{P: &tSubInner{X: 5}}
		ApplyPatch(&dst, &patch)
		if dst.P == nil || dst.P.X != 5 {
			t.Errorf("P = %+v, want &{X:5}", dst.P)
		}
	})
	t.Run("nil patch field preserves existing pointer", func(t *testing.T) {
		existing := &tSubInner{X: 9}
		dst := tPtrDst{P: existing}
		patch := tPtrPatch{P: nil}
		ApplyPatch(&dst, &patch)
		if dst.P != existing {
			t.Errorf("nil patch must preserve existing pointer, got %+v", dst.P)
		}
	})
}

func TestApplyPatch_UnexportedFieldsIgnored(t *testing.T) {
	dst := tDstUnexported{A: false}
	patch := tPatchUnexported{A: ptrBool(true)}
	// Should not panic on unexported fields and should still apply A.
	ApplyPatch(&dst, &patch)
	if dst.A != true {
		t.Errorf("A = %v, want true (exported field still applies)", dst.A)
	}
}

func TestApplyPatch_DTODrift_FieldInSrcMissingFromDst_Tolerated(t *testing.T) {
	dst := tDstWithExtraField{A: false, B: "keep"}
	patch := tPatchWithExtraField{A: ptrBool(true), C: ptrInt(99)}
	// C exists in patch but not in dst — silently ignored.
	ApplyPatch(&dst, &patch)
	if dst.A != true {
		t.Errorf("A = %v, want true", dst.A)
	}
	if dst.B != "keep" {
		t.Errorf("B = %q, want keep", dst.B)
	}
}

func TestApplyPatch_DTODrift_FieldInDstMissingFromSrc_Preserved(t *testing.T) {
	dst := tDstWithExtraField{A: false, B: "keep"}
	patch := tPatchWithExtraField{A: ptrBool(true)}
	ApplyPatch(&dst, &patch)
	if dst.B != "keep" {
		t.Errorf("B = %q, want keep (dst-only field must be preserved)", dst.B)
	}
}

// containsSubstr is a tiny helper to keep the panic-message assertions
// readable without importing strings into the test file's top imports.
// Renamed from `contains` to avoid collision with the package-level
// `contains([]string, string) bool` in stringslices.go.
func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// nonPatchableSettings are Settings fields intentionally kept OUT of the
// SettingsPatch wire surface. They are server-internal bookkeeping written
// only through dedicated atomic store methods (UpdateServerInterfaceMeta,
// SetServerPeerSecret), never through /settings/update. serverPeerSecrets in
// particular holds client WireGuard private keys, so exposing it on the
// generic PATCH would let an authenticated caller overwrite or wipe key
// material — see TestSettingsPatch_ExcludesServerSecrets.
var nonPatchableSettings = map[string]struct{}{
	"serverInterfaceMeta": {},
	"serverPeerSecrets":   {},
	// fakeip is backend-managed fakeip-tun operational state written ONLY via
	// SettingsStore.SetFakeIPState (the single-writer lifecycle). Exposing it on
	// the generic PATCH surface would let an authenticated client PUT clobber
	// the allocated OpkgTun index / provisioned flag — the exact race this
	// design eliminates by keeping the state off the settings API.
	"fakeip": {},
}

// TestSettingsPatch_ExcludesServerSecrets pins the intentional exclusion: a
// future change that "mirrors" these fields into SettingsPatch (the obvious
// way to green the mirror test) would be a security regression, so assert
// they are absent from the patch DTO.
func TestSettingsPatch_ExcludesServerSecrets(t *testing.T) {
	patchT := reflect.TypeOf(SettingsPatch{})
	for tag := range nonPatchableSettings {
		for i := 0; i < patchT.NumField(); i++ {
			if strings.Split(patchT.Field(i).Tag.Get("json"), ",")[0] == tag {
				t.Errorf("SettingsPatch must not expose %q (server-internal secret/bookkeeping)", tag)
			}
		}
	}
}

// TestSettingsPatchMirrorsSettings enforces that every exported field in
// Settings has a matching pointer field in SettingsPatch with the same
// json tag (except deliberately nonPatchableSettings). Catches drift at test
// time when a new Settings field lands without a corresponding SettingsPatch
// entry.
func TestSettingsPatchMirrorsSettings(t *testing.T) {
	settingsT := reflect.TypeOf(Settings{})
	patchT := reflect.TypeOf(SettingsPatch{})

	patchByTag := map[string]reflect.StructField{}
	for i := 0; i < patchT.NumField(); i++ {
		f := patchT.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		patchByTag[tag] = f
	}

	for i := 0; i < settingsT.NumField(); i++ {
		f := settingsT.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		if _, skip := nonPatchableSettings[tag]; skip {
			// Deliberately excluded from the wire PATCH surface — see
			// nonPatchableSettings and TestSettingsPatch_ExcludesServerSecrets.
			continue
		}
		patchF, ok := patchByTag[tag]
		if !ok {
			t.Errorf("Settings.%s (json:%q) has no corresponding field in SettingsPatch", f.Name, tag)
			continue
		}
		if patchF.Type.Kind() != reflect.Pointer {
			t.Errorf("SettingsPatch.%s (json:%q) must be a pointer, got %s", patchF.Name, tag, patchF.Type)
			continue
		}
		// For pointer-in-Settings (like ManagedServer *ManagedServer), patch
		// should be the same pointer type. For value-in-Settings, patch
		// should be *value.
		if f.Type.Kind() == reflect.Pointer {
			if patchF.Type != f.Type {
				t.Errorf("SettingsPatch.%s: expected %s, got %s", patchF.Name, f.Type, patchF.Type)
			}
		} else {
			if patchF.Type.Elem() != f.Type {
				// Nested patch struct is allowed (e.g. LoggingSettingsPatch
				// for LoggingSettings) as long as both sides are structs.
				if patchF.Type.Elem().Kind() == reflect.Struct && f.Type.Kind() == reflect.Struct {
					continue
				}
				t.Errorf("SettingsPatch.%s: expected *%s, got %s", patchF.Name, f.Type, patchF.Type)
			}
		}
	}
}

func TestLoggingSettingsPatchMirrorsLoggingSettings(t *testing.T) {
	loggingT := reflect.TypeOf(LoggingSettings{})
	patchT := reflect.TypeOf(LoggingSettingsPatch{})

	patchByTag := map[string]reflect.StructField{}
	for i := 0; i < patchT.NumField(); i++ {
		f := patchT.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		patchByTag[tag] = f
	}

	for i := 0; i < loggingT.NumField(); i++ {
		f := loggingT.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		patchF, ok := patchByTag[tag]
		if !ok {
			t.Errorf("LoggingSettings.%s (json:%q) has no corresponding field in LoggingSettingsPatch", f.Name, tag)
			continue
		}
		if patchF.Type.Kind() != reflect.Pointer {
			t.Errorf("LoggingSettingsPatch.%s (json:%q) must be a pointer, got %s", patchF.Name, tag, patchF.Type)
			continue
		}
		if patchF.Type.Elem() != f.Type {
			t.Errorf("LoggingSettingsPatch.%s: expected *%s, got %s", patchF.Name, f.Type, patchF.Type)
		}
	}
}
