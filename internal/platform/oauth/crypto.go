package oauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// encKeyBytes is the AES-256 key length.
const encKeyBytes = 32

// encryptor seals and opens refresh tokens with AES-256-GCM. A fresh random
// nonce is generated per message and prepended to the ciphertext.
type encryptor struct {
	aead cipher.AEAD
}

// newEncryptor builds an encryptor from a base64-encoded 32-byte key.
func newEncryptor(keyB64 string) (*encryptor, error) {
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	if len(key) != encKeyBytes {
		return nil, fmt.Errorf("key must be %d bytes, got %d", encKeyBytes, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return &encryptor{aead: aead}, nil
}

// encrypt returns nonce||ciphertext.
func (e *encryptor) encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("read nonce: %w", err)
	}
	return e.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// decrypt reverses encrypt. A wrong key or tampered input fails authentication.
func (e *encryptor) decrypt(ciphertext []byte) ([]byte, error) {
	ns := e.aead.NonceSize()
	if len(ciphertext) < ns {
		return nil, errors.New("ciphertext too short")
	}
	nonce, data := ciphertext[:ns], ciphertext[ns:]
	plaintext, err := e.aead.Open(nil, nonce, data, nil)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	return plaintext, nil
}
