package subscription

import "testing"

func TestRewriteForRaw(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		rewrote bool
	}{
		{
			name:    "github blob → raw.githubusercontent.com",
			in:      "https://github.com/ksenkovsolo/HardVPN-bypass-WhiteLists-/blob/main/vpn-lte/good_keys.txt",
			want:    "https://raw.githubusercontent.com/ksenkovsolo/HardVPN-bypass-WhiteLists-/main/vpn-lte/good_keys.txt",
			rewrote: true,
		},
		{
			name:    "github blob with nested path",
			in:      "https://github.com/org/repo/blob/develop/a/b/c.txt",
			want:    "https://raw.githubusercontent.com/org/repo/develop/a/b/c.txt",
			rewrote: true,
		},
		{
			name:    "github blob with commit SHA as ref",
			in:      "https://github.com/org/repo/blob/abc1234/file.txt",
			want:    "https://raw.githubusercontent.com/org/repo/abc1234/file.txt",
			rewrote: true,
		},
		{
			name:    "github blob preserves query + fragment",
			in:      "https://github.com/org/repo/blob/main/file.txt?ref=x#L1",
			want:    "https://raw.githubusercontent.com/org/repo/main/file.txt?ref=x#L1",
			rewrote: true,
		},
		{
			name:    "github raw URL is left alone",
			in:      "https://raw.githubusercontent.com/org/repo/main/file.txt",
			rewrote: false,
		},
		{
			name:    "github root path is not a blob view",
			in:      "https://github.com/org/repo",
			rewrote: false,
		},
		{
			name:    "github tree view is not a blob view",
			in:      "https://github.com/org/repo/tree/main",
			rewrote: false,
		},
		{
			name:    "gitlab blob → /-/raw/ (same host)",
			in:      "https://gitlab.com/org/repo/-/blob/main/file.txt",
			want:    "https://gitlab.com/org/repo/-/raw/main/file.txt",
			rewrote: true,
		},
		{
			name:    "self-hosted gitlab blob",
			in:      "https://git.example.com/org/repo/-/blob/main/file.txt",
			want:    "https://git.example.com/org/repo/-/raw/main/file.txt",
			rewrote: true,
		},
		{
			name:    "gitea/forgejo blob (src/branch/) → raw/branch/",
			in:      "https://codeberg.org/org/repo/src/branch/main/file.txt",
			want:    "https://codeberg.org/org/repo/raw/branch/main/file.txt",
			rewrote: true,
		},
		{
			name:    "gitea src/commit/ → raw/commit/",
			in:      "https://git.example.org/org/repo/src/commit/abc1234/file.txt",
			want:    "https://git.example.org/org/repo/raw/commit/abc1234/file.txt",
			rewrote: true,
		},
		{
			name:    "gitea src/tag/ → raw/tag/",
			in:      "https://git.example.org/org/repo/src/tag/v1.0/file.txt",
			want:    "https://git.example.org/org/repo/raw/tag/v1.0/file.txt",
			rewrote: true,
		},
		{
			name:    "plain text URL with no special prefix is unchanged",
			in:      "https://example.com/some/path.txt",
			rewrote: false,
		},
		{
			name:    "non-https scheme returned unchanged",
			in:      "ftp://example.com/path",
			rewrote: false,
		},
		{
			name:    "happ://https:// scheme stripped",
			in:      "happ://https://vpn.example.com/api/v1/client/subscribe?token=xyz123",
			want:    "https://vpn.example.com/api/v1/client/subscribe?token=xyz123",
			rewrote: true,
		},
		{
			name:    "happ:// domain converted to https://",
			in:      "happ://vpn.example.com/sub/token123",
			want:    "https://vpn.example.com/sub/token123",
			rewrote: true,
		},
		{
			name:    "clash://install-config extracted",
			in:      "clash://install-config?url=https%3A%2F%2Fsub.example.org%2Fclash.yaml",
			want:    "https://sub.example.org/clash.yaml",
			rewrote: true,
		},
		{
			name:    "sub://https:// stripped",
			in:      "sub://https://provider.net/export",
			want:    "https://provider.net/export",
			rewrote: true,
		},
		{
			name:    "landing page /happ?id= converted to /api/sub?id=",
			in:      "https://links.clovpn.org/happ?id=1353818979",
			want:    "https://links.clovpn.org/api/sub?id=1353818979",
			rewrote: true,
		},
		{
			name:    "/happ deeper in the path is left alone",
			in:      "https://provider.example/sub/happ?id=42",
			rewrote: false,
		},
		{
			name:    "happ://add/https:// stripped cleanly",
			in:      "happ://add/https://client.infomir.net/sub/bP15x63Sj0B_GjdN",
			want:    "https://client.infomir.net/sub/bP15x63Sj0B_GjdN",
			rewrote: true,
		},
		{
			name:    "malformed URL returned unchanged",
			in:      "://no-scheme",
			rewrote: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rewrote := RewriteForRaw(tc.in)
			if rewrote != tc.rewrote {
				t.Errorf("rewrote = %v, want %v (got URL: %s)", rewrote, tc.rewrote, got)
			}
			if tc.rewrote && got != tc.want {
				t.Errorf("URL = %q, want %q", got, tc.want)
			}
			if !tc.rewrote && got != tc.in {
				t.Errorf("URL changed without rewrote=true: %q → %q", tc.in, got)
			}
		})
	}
}
