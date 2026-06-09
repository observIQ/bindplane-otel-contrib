package bundle

import (
	"crypto/rsa"
	"fmt"
	"io"
	"os"
)

// IsBundleFile reads the first 4 bytes of path and returns true if they are the .bundle magic "BNDL".
// Uses os.Open(path) so the exact path is read regardless of working directory.
func IsBundleFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	b := make([]byte, 4)
	if _, err := io.ReadFull(f, b); err != nil {
		return false, err
	}
	return string(b) == BundleMagic, nil
}

// DecryptBundleFile decrypts a single-file .bundle at path using the given private key.
// The header is authenticated via AAD; any tampering invalidates decryption.
func DecryptBundleFile(privateKey *rsa.PrivateKey, path string) ([]byte, error) {
	headerJSON, env, err := ReadBundleFile(path)
	if err != nil {
		return nil, err
	}
	return DecryptEnvelopeWithAAD(privateKey, env, headerJSON)
}

// DecryptBundle decrypts a single-file .bundle at path using the given private key.
// The file must start with the BNDL magic; otherwise DecryptBundle returns an error.
func DecryptBundle(privateKey *rsa.PrivateKey, path string) ([]byte, error) {
	isBundle, err := IsBundleFile(path)
	if err != nil {
		return nil, err
	}
	if !isBundle {
		return nil, fmt.Errorf("%s: not a .bundle file (missing BNDL magic)", path)
	}
	return DecryptBundleFile(privateKey, path)
}
