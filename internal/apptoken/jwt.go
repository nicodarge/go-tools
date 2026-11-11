// Package apptoken mints and revokes GitHub App installation tokens: it signs the App's JWT with its
// private key, finds the installation and exchanges the JWT for a token scoped to the repositories
// and permissions asked for.
package apptoken

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

const (
	// jwtBackdate absorbs a runner clock running ahead of GitHub's.
	jwtBackdate = 60 * time.Second
	// jwtLifetime is counted from now, so exp - iat is ten minutes, GitHub's ceiling for a JWT.
	jwtLifetime = 9 * time.Minute
)

// parsePrivateKey reads an RSA private key in PKCS#1 ("RSA PRIVATE KEY") or PKCS#8 ("PRIVATE KEY")
// PEM form. Its errors never carry key material.
func parsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("the private key is not PEM encoded")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("the PKCS#1 private key cannot be parsed")
		}
		return key, nil
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("the PKCS#8 private key cannot be parsed")
		}
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("the PKCS#8 private key is not an RSA key")
		}
		return key, nil
	}
	return nil, fmt.Errorf("unsupported PEM block type %q: want RSA PRIVATE KEY or PRIVATE KEY", block.Type)
}

type jwtClaims struct {
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
	Iss string `json:"iss"`
}

// signJWT returns the RS256 JWT that authenticates as the App identified by iss (its client ID or
// its App ID).
func signJWT(key *rsa.PrivateKey, iss string, now time.Time) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(jwtClaims{
		Iat: now.Add(-jwtBackdate).Unix(),
		Exp: now.Add(jwtLifetime).Unix(),
		Iss: iss,
	})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", errors.New("the JWT cannot be signed with this private key")
	}
	return signingInput + "." + enc.EncodeToString(sig), nil
}
