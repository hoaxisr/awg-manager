package subscription

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"testing"
)

func TestDecryptHappLink_AllVersions(t *testing.T) {
	keys, err := getHappKeys()
	if err != nil {
		t.Fatalf("getHappKeys() failed: %v", err)
	}
	if len(keys) != 4 {
		t.Fatalf("expected 4 keys, got %d", len(keys))
	}

	testURL := "https://client.infomir.net/sub/pdVq1XnFdRF7rG5C"

	schemes := []string{"crypt", "crypt2", "crypt3", "crypt4"}

	for i, scheme := range schemes {
		t.Run(scheme, func(t *testing.T) {
			privKey := keys[i]
			pubKey := &privKey.PublicKey

			// Encrypt testURL
			maxLen := pubKey.Size() - 11
			var cipherBytes []byte
			for j := 0; j < len(testURL); j += maxLen {
				end := j + maxLen
				if end > len(testURL) {
					end = len(testURL)
				}
				chunk := []byte(testURL[j:end])
				enc, err := rsa.EncryptPKCS1v15(rand.Reader, pubKey, chunk)
				if err != nil {
					t.Fatalf("EncryptPKCS1v15 failed: %v", err)
				}
				cipherBytes = append(cipherBytes, enc...)
			}

			b64Payload := base64.RawURLEncoding.EncodeToString(cipherBytes)
			happLink := fmt.Sprintf("happ://%s/%s", scheme, b64Payload)

			if !IsHappCryptLink(happLink) {
				t.Fatalf("IsHappCryptLink(%q) returned false", happLink)
			}

			decrypted, err := DecryptHappLink(happLink)
			if err != nil {
				t.Fatalf("DecryptHappLink(%q) failed: %v", happLink, err)
			}

			if decrypted != testURL {
				t.Fatalf("expected %q, got %q", testURL, decrypted)
			}
		})
	}
}

func TestDecryptHappLink_RealUserLink(t *testing.T) {
	link := "happ://crypt4/icqCFaHatND6UTCBa1Q13aiXmjdnOquGDlyuNOUIDp6LHlcuoK2zd3CPaEEeRzgDaqe7nigQ7o9I76/XlhNX6SPXmsnbpwopCLHP+06YcGflstvafIKIp1UhsBu51K/iLBpY882OlcLsfyQNyBiVclRRMOvKhBfJXB3GiTwN0yyfcdJNDgGwUTaRBsJz6eW1SLaJhgadBLy8dps5bd0svR1jq3apMJVpbnVX/rAu4qlog8A1pnOJvCS9+LiOkKC+f+sFksJYArvvNybEy91l/N2WgA9JEbwfF+mToz7A45AsBTTxPDBvMBJCwOEHiUNgTCPlXp8qXglDVZ2mH918SpyTGe7KGB9vhlQ9kcB45m26qvICpKs8aDX+Mq0L+GerUADlOWJpRGq70DoUa8obrgUuhVwx2MHaae9Qu6bEO2kPJel5JYgTWl0utwnTRyYx1QkuQfeiva0fv9ZUG8s0XatsJ4E6em+Lpk6UQ+sWL1eaX3y9c+mFFcKQkGEqIF3U52IQejJmgBzdIgmgRw/+kmjKHQ8lP/4KI3L8dAStYJnET+iMb2FIwkDEFpR4BTxbqmiC7I5U3Z4abeyVapQXyBxPMEdI+Uh8mGyRHzDipzwF/Pw6uCkBckdEVBIUvY9UM04GKhGObIrYKQc+lmg2MHb94JvZeMvm5k7YOCeN3pA="

	decrypted, err := DecryptHappLink(link)
	if err != nil {
		t.Fatalf("DecryptHappLink failed: %v", err)
	}

	expected := "https://tunnel.shitposting.team/sub/WTPVoBbgmvJKH_8q"
	if decrypted != expected {
		t.Fatalf("expected %q, got %q", expected, decrypted)
	}
}
