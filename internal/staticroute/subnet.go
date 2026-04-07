package staticroute

import "strings"

// ParseSubnetComment splits "1.2.3.4/32 !ASTelegram" into cidr and comment.
// If no "!" is present, comment is empty.
func ParseSubnetComment(s string) (cidr, comment string) {
	s = strings.TrimSpace(s)
	idx := strings.Index(s, "!")
	if idx == -1 {
		return s, ""
	}
	cidr = strings.TrimSpace(s[:idx])
	comment = strings.TrimSpace(s[idx+1:])
	if cidr == "" && comment == "" {
		return s, ""
	}
	return cidr, comment
}

// FormatSubnetComment joins cidr and comment into "1.2.3.4/32 !ASTelegram".
// If comment is empty or whitespace-only, returns just cidr.
func FormatSubnetComment(cidr, comment string) string {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return cidr
	}
	return cidr + " !" + comment
}
