package ndms

import "testing"

// Переходное состояние нельзя читать ни как «включил», ни как «выключил»:
// conf=running однократен, и вето по pending потеряло бы настоящее включение
// из веб-интерфейса роутера.
func TestConfIntent_PendingIsNotAnAnswer(t *testing.T) {
	for _, tc := range []struct {
		layer string
		up    bool
		known bool
	}{
		{"running", true, true},
		{"disabled", false, true},
		{"pending", false, false},
		{"", false, false},
	} {
		t.Run(tc.layer, func(t *testing.T) {
			up, known := InterfaceDetails{ConfLayer: tc.layer}.ConfIntent()
			if up != tc.up || known != tc.known {
				t.Errorf("conf=%q → up=%v known=%v, ждали up=%v known=%v", tc.layer, up, known, tc.up, tc.known)
			}
		})
	}
}
