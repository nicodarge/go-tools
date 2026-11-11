package apptoken

import (
	"strings"
	"testing"
	"time"
)

func TestJWTClaimsAndSignature(t *testing.T) {
	a, b := testKeys(t)
	now := time.Unix(fixedNow, 0)
	token, err := signJWT(a, "Iv23liTESTCLIENT", now)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifyJWT(token, &a.PublicKey)
	if err != nil {
		t.Fatalf("the signature does not verify with the public key: %v", err)
	}
	if claims.Iss != "Iv23liTESTCLIENT" {
		t.Errorf("iss = %q", claims.Iss)
	}
	if claims.Iat != fixedNow-60 {
		t.Errorf("iat = %d, want now - 60 s = %d", claims.Iat, fixedNow-60)
	}
	if claims.Exp <= fixedNow || claims.Exp-claims.Iat > 600 {
		t.Errorf("exp = %d: want after now and at most 600 s after iat (%d)", claims.Exp, claims.Iat)
	}
	if _, err = verifyJWT(token, &b.PublicKey); err == nil {
		t.Error("the signature verifies with another App's public key")
	}
	parts := strings.Split(token, ".")
	forged, err := signJWT(a, "someone-else", now)
	if err != nil {
		t.Fatal(err)
	}
	swapped := parts[0] + "." + strings.Split(forged, ".")[1] + "." + parts[2]
	if _, err := verifyJWT(swapped, &a.PublicKey); err == nil {
		t.Error("a JWT whose claims were swapped still verifies")
	}
}

func TestParsePrivateKey(t *testing.T) {
	a, _ := testKeys(t)
	for label, data := range map[string]string{
		"PKCS#1":                  pkcs1PEM(a),
		"PKCS#8":                  pkcs8PEM(t, a),
		"PKCS#1 with CRLF":        strings.ReplaceAll(pkcs1PEM(a), "\n", "\r\n"),
		"PKCS#8 with leading gap": "\n\n" + pkcs8PEM(t, a),
	} {
		key, err := parsePrivateKey([]byte(data))
		if err != nil {
			t.Errorf("%s: %v", label, err)
			continue
		}
		if !key.Equal(a) {
			t.Errorf("%s: parsed another key", label)
		}
	}
}

func TestParsePrivateKeyRefusals(t *testing.T) {
	a, _ := testKeys(t)
	good := pkcs1PEM(a)
	lines := strings.Split(good, "\n")
	corrupt := func(typ string) string { return armor(typ, lines[1]+"\n"+lines[2]) }
	for label, data := range map[string]string{
		"empty":                   "",
		"not PEM":                 "this is not a key",
		"a bare base64 body":      pemBody(good),
		"PKCS#1 corrupt DER":      corrupt("RSA PRIVATE KEY"),
		"PKCS#8 corrupt DER":      corrupt("PRIVATE KEY"),
		"an EC key in PKCS#8":     ecKeyPEM(t),
		"an encrypted PKCS#8 key": armor("ENCRYPTED PRIVATE KEY", lines[1]),
		"a certificate":           armor("CERTIFICATE", lines[1]),
		"a public key":            armor("PUBLIC KEY", lines[1]),
	} {
		key, err := parsePrivateKey([]byte(data))
		if err == nil || key != nil {
			t.Errorf("%s: accepted", label)
			continue
		}
		if strings.Contains(err.Error(), lines[1]) || strings.Contains(err.Error(), lines[2]) {
			t.Errorf("%s: the error quotes key material: %v", label, err)
		}
	}
}
