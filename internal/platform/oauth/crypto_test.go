package oauth

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func keyB64(t *testing.T, b byte) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, encKeyBytes))
}

func TestEncryptor_roundTrip(t *testing.T) {
	enc, err := newEncryptor(keyB64(t, 0x01))
	require.NoError(t, err)

	plaintext := []byte("1//refresh-token-value")
	ct, err := enc.encrypt(plaintext)
	require.NoError(t, err)
	require.NotEqual(t, plaintext, ct, "ciphertext must differ from plaintext")
	require.NotContains(t, string(ct), string(plaintext), "plaintext must not appear in ciphertext")

	got, err := enc.decrypt(ct)
	require.NoError(t, err)
	require.Equal(t, plaintext, got)
}

func TestEncryptor_differentNoncePerCall(t *testing.T) {
	enc, err := newEncryptor(keyB64(t, 0x02))
	require.NoError(t, err)

	a, err := enc.encrypt([]byte("same"))
	require.NoError(t, err)
	b, err := enc.encrypt([]byte("same"))
	require.NoError(t, err)
	require.NotEqual(t, a, b, "a random nonce must make ciphertexts differ")
}

func TestEncryptor_wrongKeyFails(t *testing.T) {
	enc1, err := newEncryptor(keyB64(t, 0x03))
	require.NoError(t, err)
	enc2, err := newEncryptor(keyB64(t, 0x04))
	require.NoError(t, err)

	ct, err := enc1.encrypt([]byte("secret"))
	require.NoError(t, err)

	_, err = enc2.decrypt(ct)
	require.Error(t, err, "a different key must fail authentication")
}

func TestNewEncryptor_badKey(t *testing.T) {
	_, err := newEncryptor("not-base64!!!")
	require.Error(t, err)

	_, err = newEncryptor(base64.StdEncoding.EncodeToString([]byte("too-short")))
	require.ErrorContains(t, err, "key must be 32 bytes")
}
