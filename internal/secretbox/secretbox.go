// Package secretbox owns secret envelope encryption for Porter (task G4).
//
// One AES-256-GCM box keyed by server key material. Both the API (which mints
// secrets) and the runtime (which injects them into guests at boot) share this
// package so the key derivation lives in exactly one place. Ciphertext format:
// nonce‖ciphertext (GCM), transported base64 or raw bytes by callers.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
)

// Key derives the stable 32-byte box key from server key material.
// It is plain SHA-256 for compatibility with secrets minted before this
// package existed. Authorization credentials must never be reused for this.
func Key(material string) []byte {
	sum := sha256.Sum256([]byte(material))
	return sum[:]
}

// Seal encrypts plaintext.
func Seal(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// MMDSPayload builds the MMDS V2 JSON subtree for one project's secrets.
// Shape: {"porter": {"project_id": ..., "secrets": {NAME: value}}}. Values stay
// in memory only; callers must never log the returned map.
func MMDSPayload(projectID string, secrets map[string]string) map[string]any {
	env := make(map[string]any, len(secrets))
	for k, v := range secrets {
		env[k] = v
	}
	return map[string]any{"porter": map[string]any{
		"project_id": projectID,
		"secrets":    env,
	}}
}

// Open decrypts a box produced by Seal.
func Open(key, box []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(box) < gcm.NonceSize() {
		return nil, fmt.Errorf("secretbox: box too short")
	}
	nonce, ciphertext := box[:gcm.NonceSize()], box[gcm.NonceSize():]
	raw, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("secretbox: open: %w", err)
	}
	return raw, nil
}
