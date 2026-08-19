package subscription

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestDecryptHappLink_NotConfigured(t *testing.T) {
	k := newHappKeys(filepath.Join(t.TempDir(), "happ_keys.json"))

	link := "happ://crypt/YWJj"
	_, err := k.decrypt(link)
	if !errors.Is(err, ErrHappKeysNotConfigured) {
		t.Fatalf("expected ErrHappKeysNotConfigured, got %v", err)
	}
}

func TestDecryptHappLink_DynamicKeys(t *testing.T) {
	// Generate 4 synthetic test keys
	var b64Keys []string
	var privKeys []*rsa.PrivateKey

	for i := 0; i < 4; i++ {
		pk, err := rsa.GenerateKey(rand.Reader, 1024)
		if err != nil {
			t.Fatalf("GenerateKey failed: %v", err)
		}
		der := x509.MarshalPKCS1PrivateKey(pk)
		b64 := base64.StdEncoding.EncodeToString(der)
		b64Keys = append(b64Keys, b64)
		privKeys = append(privKeys, pk)
	}

	k := newHappKeys(filepath.Join(t.TempDir(), "happ_keys.json"))
	if err := k.set(b64Keys); err != nil {
		t.Fatalf("set keys failed: %v", err)
	}

	testURL := "https://client.example.com/sub/test-token-12345"
	schemes := []string{"crypt", "crypt2", "crypt3", "crypt4"}

	for i, scheme := range schemes {
		t.Run(scheme, func(t *testing.T) {
			privKey := privKeys[i]
			pubKey := &privKey.PublicKey

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

			decrypted, err := k.decrypt(happLink)
			if err != nil {
				t.Fatalf("decrypt(%q) failed: %v", happLink, err)
			}

			if decrypted != testURL {
				t.Fatalf("expected %q, got %q", testURL, decrypted)
			}
		})
	}
}

func TestParseHappKeysInput(t *testing.T) {
	jsonInput := `[
		"MIICXwIBAAKBgQCxsS7PUq1biQlVD92rf6eXKr9oG1/SrYx3qWahZP+Jq35m4Wb/Z+mB6eBWrPzJ/zZpZLWLQorcvOKt+sLaCHyH1HLNkti4jlaEQX6x97XgBm8GK08+lLLWquFDhWRNxsrfzJyNdpVopzBRmCJKTc8ObYyPbrv9T35a8Kd5WqjnUwIDAQABAoGBAJoqe85skPPF5U7jwRM2YhUJhZ+xgGWtJR3834pPslWjcLuZ/F7DrRiF7ZnF5FztDCxMsCXuycPSLWl9EulQS5mrL/fnwpK2jVE8O1Em9RsBOOrWwzuZnAuooRIb/8zC0fvH2oGkk60zSKycMe69uvYUDjhvULX2Spjmf9CS9/HhAkEA3I797En/DrpAZz6NM4GqZ1mkH0kEX/kAHLP1lBgYL1kVK455EG/ecJkMJmtK7A+fWw0N0IcxrpYAbbOAo19vjwJBAM4+0MAZ8TIZUk6Rs2gYUo04A6mYUy5MWtRa9pyFIgD71oHDR+1jrnPLqQyCj0tfbZBc1iVgsisJBpocC8sKaf0CQQDRNd3Mxb/nY2p1xJLBmaxezlvsxSEePB4MG/PFXzmJqBF5uHJD0imIWtR4mOt/ka4R+wbwl1zcAzMy28MYtQ0nAkEAuUILWML0uL+uAw01TeerH1aVU52T+h5z6BPdOTMNHD0arWywCzhi13i03JvaAyYw0F/Tq7dz0txEpeFTZopwMQJBANnHbzB87/xTjDQA4/L8sSU8m0vM1nRWmJIaAC94pcM+KDGLnbBhWrvZGy8Zg8vQwNvdvCLvylk0jVTTFqW3ibM="
	]`
	keys, err := ParseHappKeysInput(jsonInput)
	if err != nil {
		t.Fatalf("ParseHappKeysInput failed: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
}
