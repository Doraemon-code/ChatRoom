package protocol

import (
	"encoding/base64"
	"strings"
	"testing"
)

const (
	validID   = "01890f6e-74e8-4c72-8f9c-61fca85b71e7"
	validRoom = "0001"
)

func validPublicKeyString() string {
	key := make([]byte, 65)
	key[0] = 4
	return base64.RawURLEncoding.EncodeToString(key)
}

func TestStrictDecode(t *testing.T) {
	publicKey := validPublicKeyString()
	good := `{"v":1,"type":"join_room","roomId":"` + validRoom + `","ephemeralId":"` + validID + `","nickname":"Guest-A7F2","publicKey":"` + publicKey + `"}`
	if _, err := Decode([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(good, `}`, `,"extra":1}`, 1),
		good + `{}`,
		strings.Replace(good, `"v":1`, `"v":2`, 1),
		strings.Replace(good, validRoom, "0009", 1),
		strings.Replace(good, validRoom, "0012", 1),
		strings.Replace(good, validRoom, "000", 1),
		strings.Replace(good, publicKey, "AAAA", 1),
	} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestCiphertextLimits(t *testing.T) {
	iv := base64.RawURLEncoding.EncodeToString(make([]byte, 12))
	head := `{"v":1,"type":"ciphertext","roomId":"` + validRoom + `","senderEphemeralId":"` + validID + `","messageId":"11890f6e-74e8-4c72-8f9c-61fca85b71e7","iv":"` + iv + `","ciphertext":"`
	tail := `"}`
	body := func(size int) string { return head + base64.RawURLEncoding.EncodeToString(make([]byte, size)) + tail }

	if _, err := Decode([]byte(body(16))); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode([]byte(body(600_000))); err != nil {
		t.Fatalf("image-sized ciphertext rejected: %v", err)
	}
	if _, err := Decode([]byte(body(MaxPlaintext + 16))); err != nil {
		t.Fatalf("max ciphertext rejected: %v", err)
	}
	if _, err := Decode([]byte(body(MaxPlaintext + 17))); err == nil {
		t.Fatal("oversize ciphertext accepted")
	}
	if _, err := Decode(make([]byte, MaxFrameBytes+1)); err == nil {
		t.Fatal("oversize frame accepted")
	}
}

func TestRenameSupportsMobileUserChosenNamesSafely(t *testing.T) {
	if _, err := Decode([]byte(`{"v":1,"type":"rename","nickname":"福尔摩斯"}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"v":1,"type":"rename","nickname":"A"}`,
		`{"v":1,"type":"rename","nickname":" Alice"}`,
		`{"v":1,"type":"rename","nickname":"Alice\nBob"}`,
	} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Fatalf("accepted unsafe nickname: %s", bad)
		}
	}
}

func TestFileTokenRequestStrictDecode(t *testing.T) {
	if _, err := Decode([]byte(`{"v":1,"type":"file_token_request"}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"v":1,"type":"file_token_request","extra":1}`,
		`{"v":1,"type":"file_token_request"}{}`,
		`{"v":1,"type":"file_token"}`,
		`{"v":1,"type":"file_expired"}`,
	} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestFileTokenValidation(t *testing.T) {
	good := FileToken{V: 1, Type: "file_token", Token: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ExpiresIn: 60}
	if err := validate(&good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []FileToken{
		{V: 1, Type: "file_token", Token: base64.RawURLEncoding.EncodeToString(make([]byte, 31)), ExpiresIn: 60},
		{V: 1, Type: "file_token", Token: base64.RawURLEncoding.EncodeToString(make([]byte, 33)), ExpiresIn: 60},
		{V: 1, Type: "file_token", Token: "not_base64!!", ExpiresIn: 60},
		{V: 1, Type: "file_token", Token: good.Token, ExpiresIn: 0},
		{V: 1, Type: "file_token", Token: good.Token, ExpiresIn: 601},
	} {
		if err := validate(&bad); err == nil {
			t.Fatalf("accepted token: %+v", bad)
		}
	}
}

func TestFileExpiredValidation(t *testing.T) {
	if err := validate(&FileExpired{V: 1, Type: "file_expired", FileID: validID}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "not-a-uuid", strings.ToUpper(validID)} {
		if err := validate(&FileExpired{V: 1, Type: "file_expired", FileID: bad}); err == nil {
			t.Fatalf("accepted file id %q", bad)
		}
	}
}
