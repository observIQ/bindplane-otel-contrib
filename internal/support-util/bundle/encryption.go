package bundle

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// Encryption defines the interface for encrypting and decrypting data
type Encryption interface {
	Encrypt(data []byte) ([]byte, error)
	Decrypt(data []byte) ([]byte, error)
}

type Envelope struct {
	EncryptedFile []byte // AES-GCM encrypted zip
	EncryptedKey  []byte // RSA-OAEP encrypted AES key
	Nonce         []byte // AES-GCM nonce
	KeyID         string // optional UUID for tracking
}

// LoadRSAPublicKey loads an RSA public key from a PEM file
func LoadRSAPublicKey(path string) (*rsa.PublicKey, error) {
	dir, base := filepath.Dir(path), filepath.Base(path)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	pemBytes, err := root.ReadFile(base)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("invalid PEM block")
	}

	// Handles "PUBLIC KEY" (PKIX)
	if pub, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if rsaPub, ok := pub.(*rsa.PublicKey); ok {
			return rsaPub, nil
		}
	}

	// Handles "RSA PUBLIC KEY" (PKCS#1)
	if rsaPub, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return rsaPub, nil
	}

	return nil, errors.New("unsupported public key format")
}

func EncryptAESGCM(plaintext, key []byte) (ciphertext, nonce []byte, err error) {
	return EncryptAESGCMWithAAD(plaintext, key, nil)
}

// EncryptAESGCMWithAAD encrypts with AES-GCM; aad is optional (e.g. header bytes for authentication).
func EncryptAESGCMWithAAD(plaintext, key, aad []byte) (ciphertext, nonce []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}

	nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}

	ciphertext = gcm.Seal(nil, nonce, plaintext, aad)
	return ciphertext, nonce, nil
}

func EncryptEnvelope(
	publicKey *rsa.PublicKey,
	zipData []byte,
) (*Envelope, error) {
	return EncryptEnvelopeWithAAD(publicKey, zipData, nil)
}

// EncryptEnvelopeWithAAD encrypts the payload with AES-GCM using headerBytes as AAD when non-nil.
// The exact header bytes must be passed to DecryptEnvelopeWithAAD when decrypting.
func EncryptEnvelopeWithAAD(
	publicKey *rsa.PublicKey,
	zipData []byte,
	headerBytes []byte,
) (*Envelope, error) {
	// 1. Generate strong symmetric key
	fileKey := make([]byte, 32) // AES-256
	if _, err := rand.Read(fileKey); err != nil {
		return nil, err
	}

	// Optional tracking ID
	keyID := uuid.NewString()

	// 2. Encrypt zip with AES-GCM (with optional AAD)
	encryptedFile, nonce, err := EncryptAESGCMWithAAD(zipData, fileKey, headerBytes)
	if err != nil {
		return nil, err
	}

	// 3. Encrypt file key with RSA-OAEP
	encryptedKey, err := rsa.EncryptOAEP(
		sha256.New(),
		rand.Reader,
		publicKey,
		fileKey,
		nil,
	)
	if err != nil {
		return nil, err
	}

	return &Envelope{
		EncryptedFile: encryptedFile,
		EncryptedKey:  encryptedKey,
		Nonce:         nonce,
		KeyID:         keyID,
	}, nil
}

type EncryptionOptions struct {
	Enabled       bool
	PublicKeyPath string // optional
	PublicKeyPEM  []byte // optional (inline key)
}

func loadPublicKey(enc EncryptionOptions) (*rsa.PublicKey, error) {
	switch {
	case enc.PublicKeyPath != "":
		return LoadRSAPublicKey(enc.PublicKeyPath)

	case len(enc.PublicKeyPEM) > 0:
		return ParseRSAPublicKey(enc.PublicKeyPEM)

	default:
		return nil, fmt.Errorf("encryption enabled but no public key provided")
	}
}

// RSAPublicKeyFingerprint returns the spec-style fingerprint "sha256:base64" for the public key.
func RSAPublicKeyFingerprint(pub *rsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return "sha256:" + base64.StdEncoding.EncodeToString(sum[:]), nil
}

func ParseRSAPublicKey(pemBytes []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("invalid public key PEM")
	}

	if pub, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if rsaPub, ok := pub.(*rsa.PublicKey); ok {
			return rsaPub, nil
		}
	}

	if rsaPub, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return rsaPub, nil
	}

	return nil, fmt.Errorf("unsupported public key format")
}

// LoadRSAPrivateKey loads an RSA private key from a PEM file (PKCS#1 or PKCS#8).
func LoadRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
	dir, base := filepath.Dir(path), filepath.Base(path)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	pemBytes, err := root.ReadFile(base)
	if err != nil {
		return nil, err
	}
	return ParseRSAPrivateKey(pemBytes)
}

// ParseRSAPrivateKey parses an RSA private key from PEM bytes (PKCS#1 or PKCS#8).
func ParseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("invalid PEM block")
	}

	// PKCS#8 (e.g. "PRIVATE KEY")
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if pk, ok := key.(*rsa.PrivateKey); ok {
			return pk, nil
		}
	}

	// PKCS#1 (e.g. "RSA PRIVATE KEY")
	if pk, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return pk, nil
	}

	return nil, errors.New("unsupported private key format")
}

// DecryptAESGCM decrypts AES-GCM ciphertext with the given key and nonce.
func DecryptAESGCM(ciphertext, key, nonce []byte) ([]byte, error) {
	return DecryptAESGCMWithAAD(ciphertext, key, nonce, nil)
}

// DecryptAESGCMWithAAD decrypts AES-GCM ciphertext; aad must match the value used at seal time.
func DecryptAESGCMWithAAD(ciphertext, key, nonce, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}

// DecryptEnvelope decrypts an envelope: RSA-OAEP unwraps the AES key, then AES-GCM decrypts the file.
func DecryptEnvelope(privateKey *rsa.PrivateKey, env *Envelope) ([]byte, error) {
	return DecryptEnvelopeWithAAD(privateKey, env, nil)
}

// DecryptEnvelopeWithAAD decrypts an envelope using the given AAD (e.g. header bytes) for AES-GCM.
// AAD must match the bytes passed to EncryptEnvelopeWithAAD.
func DecryptEnvelopeWithAAD(privateKey *rsa.PrivateKey, env *Envelope, aad []byte) ([]byte, error) {
	fileKey, err := rsa.DecryptOAEP(
		sha256.New(),
		rand.Reader,
		privateKey,
		env.EncryptedKey,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("decrypt envelope key: %w", err)
	}
	plaintext, err := DecryptAESGCMWithAAD(env.EncryptedFile, fileKey, env.Nonce, aad)
	if err != nil {
		return nil, fmt.Errorf("decrypt envelope payload: %w", err)
	}
	return plaintext, nil
}
