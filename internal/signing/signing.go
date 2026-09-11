// Package signing owns the Ed25519 issuing key. The private key is read from a
// local PEM file at startup; it is never accepted through or returned by the
// HTTP API.
package signing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

type Signer struct {
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
	KeyID   string
}

// KeyID derives a stable key identifier from the SubjectPublicKeyInfo bytes.
// Signatures and public keys are cross-checked through this identifier, so a
// snapshot signed with a different key can never validate at startup.
func KeyID(public ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), nil
}

// LoadPrivateKey reads an Ed25519 private key from a PKCS#8 PEM file.
func LoadPrivateKey(path string) (*Signer, error) {
	if path == "" {
		return nil, fmt.Errorf("未配置签发私钥：请通过 -signing-key 或 STRATA_SIGNING_KEY 指向 Ed25519 PEM 文件（可用 strata-keygen 生成）")
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("无法读取签发私钥 %s: %w", path, err)
	}
	if info, statErr := os.Stat(path); statErr == nil {
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			return nil, fmt.Errorf("签发私钥 %s 权限过宽（%04o）：仅允许属主读写（如 0600），拒绝启动", path, perm)
		}
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("签发私钥 %s 不是有效的 PEM 文件", path)
	}
	if block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("签发私钥 %s 的 PEM 类型必须为 PRIVATE KEY，实际为 %s", path, block.Type)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("签发私钥 %s 无法解析为 PKCS#8: %w", path, err)
	}
	private, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("签发私钥 %s 不是 Ed25519 密钥", path)
	}
	public, ok := private.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("签发私钥 %s 的公钥类型错误", path)
	}
	id, err := KeyID(public)
	if err != nil {
		return nil, err
	}
	return &Signer{Private: private, Public: public, KeyID: id}, nil
}

// PublicKeyPEM encodes the public key in SubjectPublicKeyInfo PEM form, which is
// the artifact handed to external verifiers.
func (s *Signer) PublicKeyPEM() ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(s.Public)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

func (s *Signer) Sign(message []byte) []byte {
	return ed25519.Sign(s.Private, message)
}

// ParsePublicKeyPEM reads a SubjectPublicKeyInfo PEM public key.
func ParsePublicKeyPEM(raw []byte) (ed25519.PublicKey, string, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, "", fmt.Errorf("公钥不是有效的 PEM 文件")
	}
	if block.Type != "PUBLIC KEY" {
		return nil, "", fmt.Errorf("公钥 PEM 类型必须为 PUBLIC KEY，实际为 %s", block.Type)
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("公钥无法解析: %w", err)
	}
	public, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, "", fmt.Errorf("公钥不是 Ed25519 密钥")
	}
	id, err := KeyID(public)
	if err != nil {
		return nil, "", err
	}
	return public, id, nil
}

type Verifier struct {
	Public ed25519.PublicKey
	KeyID  string
}

func NewVerifier(public ed25519.PublicKey) (*Verifier, error) {
	id, err := KeyID(public)
	if err != nil {
		return nil, err
	}
	return &Verifier{Public: public, KeyID: id}, nil
}

// Verify reports whether signature is valid for message under this public key.
func (v *Verifier) Verify(message, signature []byte) bool {
	if l := len(signature); l != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(v.Public, message, signature)
}
