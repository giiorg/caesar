package secrets_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/secrets"
)

func testKey(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestParseKeyset_AcceptsSingleAndMultipleKeys(t *testing.T) {
	one, two := testKey(t), testKey(t)
	ks, err := secrets.ParseKeyset("prod:" + one)
	if err != nil {
		t.Fatal(err)
	}
	if ks.ActiveKeyID() != "prod" {
		t.Fatalf("active = %q, want prod", ks.ActiveKeyID())
	}
	ks, err = secrets.ParseKeyset("next:" + two + ",prod:" + one)
	if err != nil {
		t.Fatal(err)
	}
	if ks.ActiveKeyID() != "next" {
		t.Fatalf("active = %q, want next (first entry)", ks.ActiveKeyID())
	}
}

func TestParseKeyset_RejectsMalformedInput(t *testing.T) {
	good := testKey(t)
	cases := []struct{ name, in string }{
		{"empty", ""},
		{"no colon", good},
		{"empty id", ":" + good},
		{"bad id chars", "prod key:" + good},
		{"id too long", strings.Repeat("a", 65) + ":" + good},
		{"not base64", "prod:@@@@"},
		{"wrong key length", "prod:" + base64.StdEncoding.EncodeToString([]byte("short"))},
		{"duplicate id", "prod:" + good + ",prod:" + good},
	}
	for _, c := range cases {
		if _, err := secrets.ParseKeyset(c.in); err == nil {
			t.Errorf("%s: no error for %q", c.name, c.in)
		}
	}
}

func TestSealOpen_RoundTripsUnderActiveKey(t *testing.T) {
	ks, err := secrets.ParseKeyset("prod:" + testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	plain, aad := []byte("hunter2-token"), []byte("credential-id")
	env, err := ks.Seal(plain, aad)
	if err != nil {
		t.Fatal(err)
	}
	if env.KeyID != "prod" {
		t.Fatalf("key id = %q", env.KeyID)
	}
	if bytes.Contains(env.Ciphertext, plain) {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := ks.Open(env, aad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip = %q, want %q", got, plain)
	}
}

func TestOpen_DecryptOnlyKeyStillOpens(t *testing.T) {
	old := "old:" + testKey(t)
	ksOld, err := secrets.ParseKeyset(old)
	if err != nil {
		t.Fatal(err)
	}
	env, err := ksOld.Seal([]byte("sealed under old"), []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	ksNew, err := secrets.ParseKeyset("new:" + testKey(t) + "," + old)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ksNew.Open(env, []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "sealed under old" {
		t.Fatalf("got %q", got)
	}
}

func TestOpen_FailsClosed(t *testing.T) {
	ks, err := secrets.ParseKeyset("prod:" + testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	env, err := ks.Seal([]byte("secret"), []byte("right-aad"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ks.Open(env, []byte("wrong-aad")); err == nil {
		t.Error("wrong AAD must fail")
	}
	unknown := env
	unknown.KeyID = "ghost"
	if _, err := ks.Open(unknown, []byte("right-aad")); err == nil {
		t.Error("unknown key id must fail")
	}
	tampered := env
	tampered.Ciphertext = append([]byte(nil), env.Ciphertext...)
	tampered.Ciphertext[len(tampered.Ciphertext)-1] ^= 0x01
	if _, err := ks.Open(tampered, []byte("right-aad")); err == nil {
		t.Error("tampered ciphertext must fail")
	}
	short := env
	short.WrappedDEK = short.WrappedDEK[:4]
	if _, err := ks.Open(short, []byte("right-aad")); err == nil {
		t.Error("truncated wrapped dek must fail")
	}
}
