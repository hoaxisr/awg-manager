package mailru

import "testing"

// A read-only public link ("edit": false) must not get saveChanges: the server answers it by
// closing the connection (seen against the live service, docx, link without edit rights).
func TestCanEdit(t *testing.T) {
	for _, tc := range []struct {
		name string
		perm map[string]interface{}
		want bool
	}{
		{"nothing known", nil, true},
		{"no edit key", map[string]interface{}{"download": true}, true},
		{"editable", map[string]interface{}{"edit": true, "download": true}, true},
		{"read-only", map[string]interface{}{"edit": false, "download": true}, false},
		{"odd value", map[string]interface{}{"edit": "false"}, true},
	} {
		if got := canEdit(tc.perm); got != tc.want {
			t.Errorf("%s: canEdit = %v, want %v", tc.name, got, tc.want)
		}
	}
}
