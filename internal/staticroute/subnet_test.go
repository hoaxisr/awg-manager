package staticroute

import "testing"

func TestParseSubnetComment(t *testing.T) {
	tests := []struct {
		input       string
		wantCIDR    string
		wantComment string
	}{
		{"1.2.3.4/32 !ASTelegram", "1.2.3.4/32", "ASTelegram"},
		{"10.0.0.0/8", "10.0.0.0/8", ""},
		{"172.16.0.0/12 !AS Google CDN", "172.16.0.0/12", "AS Google CDN"},
		{"  192.168.1.0/24  !Test  ", "192.168.1.0/24", "Test"},
		{"!", "!", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			cidr, comment := ParseSubnetComment(tt.input)
			if cidr != tt.wantCIDR {
				t.Errorf("cidr = %q, want %q", cidr, tt.wantCIDR)
			}
			if comment != tt.wantComment {
				t.Errorf("comment = %q, want %q", comment, tt.wantComment)
			}
		})
	}
}

func TestFormatSubnetComment(t *testing.T) {
	tests := []struct {
		cidr    string
		comment string
		want    string
	}{
		{"1.2.3.4/32", "ASTelegram", "1.2.3.4/32 !ASTelegram"},
		{"10.0.0.0/8", "", "10.0.0.0/8"},
		{"172.16.0.0/12", "  ", "172.16.0.0/12"},
	}
	for _, tt := range tests {
		t.Run(tt.cidr+"/"+tt.comment, func(t *testing.T) {
			got := FormatSubnetComment(tt.cidr, tt.comment)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
