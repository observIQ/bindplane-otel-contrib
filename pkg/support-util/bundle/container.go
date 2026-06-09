package bundle

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	// BundleMagic is the 4-byte magic identifier for .bundle files.
	BundleMagic = "BNDL"
	// BundleVersion is the current container format version (major).
	BundleVersion uint16 = 1
)

// Max sizes for sanity checks when reading (prevent OOM).
const (
	maxHeaderLen       = 1 << 20  // 1 MiB
	maxEncryptedKeyLen = 1 << 10  // 1 KiB (RSA-2048 ciphertext is 256 bytes)
	maxNonceLen        = 24       // GCM typical nonce size
	maxPayloadLen      = 1 << 34  // 16 GiB
)

// WriteBundleFile writes a single .bundle file with the spec layout:
// Magic (4) | Version (2) | HeaderLen (4) | Header | EncKeyLen (2) | EncKey | NonceLen (1) | Nonce | PayloadLen (8) | Payload
func WriteBundleFile(path string, headerJSON []byte, env *Envelope) error {
	dir, base := filepath.Dir(path), filepath.Base(path)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open output dir: %w", err)
	}
	defer root.Close()

	f, err := root.Create(base)
	if err != nil {
		return fmt.Errorf("create bundle file: %w", err)
	}
	defer f.Close()

	encKeyLen := uint16(len(env.EncryptedKey))
	nonceLen := uint8(len(env.Nonce))
	payloadLen := uint64(len(env.EncryptedFile))

	// Fixed and length fields
	if _, err := f.Write([]byte(BundleMagic)); err != nil {
		return err
	}
	if err := binary.Write(f, binary.BigEndian, BundleVersion); err != nil {
		return err
	}
	if err := binary.Write(f, binary.BigEndian, uint32(len(headerJSON))); err != nil {
		return err
	}
	if _, err := f.Write(headerJSON); err != nil {
		return err
	}
	if err := binary.Write(f, binary.BigEndian, encKeyLen); err != nil {
		return err
	}
	if _, err := f.Write(env.EncryptedKey); err != nil {
		return err
	}
	if err := binary.Write(f, binary.BigEndian, nonceLen); err != nil {
		return err
	}
	if _, err := f.Write(env.Nonce); err != nil {
		return err
	}
	if err := binary.Write(f, binary.BigEndian, payloadLen); err != nil {
		return err
	}
	if _, err := f.Write(env.EncryptedFile); err != nil {
		return err
	}

	return nil
}

// ReadBundleFile parses a .bundle file and returns the raw header bytes and envelope.
// Uses os.Open(path) so the exact path is used regardless of working directory.
func ReadBundleFile(path string) (headerJSON []byte, env *Envelope, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open bundle file: %w", err)
	}
	defer f.Close()

	return readBundleFromReader(f)
}

// ReadBundleHeader reads only the plaintext header from a .bundle file (magic through header bytes).
// It does not read the encrypted key, nonce, or payload. Use this to get org_id or other header
// fields without reading or decrypting the full file.
func ReadBundleHeader(path string) (*BundleHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open bundle file: %w", err)
	}
	defer f.Close()

	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return nil, fmt.Errorf("read magic: %w", err)
	}
	if string(magic) != BundleMagic {
		return nil, errors.New("invalid bundle: bad magic")
	}

	var version uint16
	if err := binary.Read(f, binary.BigEndian, &version); err != nil {
		return nil, fmt.Errorf("read version: %w", err)
	}
	if version != BundleVersion {
		return nil, fmt.Errorf("unsupported bundle version %d (supported: %d)", version, BundleVersion)
	}

	var headerLen uint32
	if err := binary.Read(f, binary.BigEndian, &headerLen); err != nil {
		return nil, fmt.Errorf("read header length: %w", err)
	}
	if headerLen > maxHeaderLen {
		return nil, fmt.Errorf("header length %d exceeds max %d", headerLen, maxHeaderLen)
	}
	headerJSON := make([]byte, headerLen)
	if _, err := io.ReadFull(f, headerJSON); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	return ParseHeader(headerJSON)
}

func readBundleFromReader(r io.Reader) (headerJSON []byte, env *Envelope, err error) {
	magic := make([]byte, 4)
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, nil, fmt.Errorf("read magic: %w", err)
	}
	if string(magic) != BundleMagic {
		return nil, nil, errors.New("invalid bundle: bad magic")
	}

	var version uint16
	if err := binary.Read(r, binary.BigEndian, &version); err != nil {
		return nil, nil, fmt.Errorf("read version: %w", err)
	}
	// Reject unknown major version
	if version != BundleVersion {
		return nil, nil, fmt.Errorf("unsupported bundle version %d (supported: %d)", version, BundleVersion)
	}

	var headerLen uint32
	if err := binary.Read(r, binary.BigEndian, &headerLen); err != nil {
		return nil, nil, fmt.Errorf("read header length: %w", err)
	}
	if headerLen > maxHeaderLen {
		return nil, nil, fmt.Errorf("header length %d exceeds max %d", headerLen, maxHeaderLen)
	}
	headerJSON = make([]byte, headerLen)
	if _, err := io.ReadFull(r, headerJSON); err != nil {
		return nil, nil, fmt.Errorf("read header: %w", err)
	}

	var encKeyLen uint16
	if err := binary.Read(r, binary.BigEndian, &encKeyLen); err != nil {
		return nil, nil, fmt.Errorf("read encrypted key length: %w", err)
	}
	if encKeyLen > maxEncryptedKeyLen {
		return nil, nil, fmt.Errorf("encrypted key length %d exceeds max %d", encKeyLen, maxEncryptedKeyLen)
	}
	encKey := make([]byte, encKeyLen)
	if _, err := io.ReadFull(r, encKey); err != nil {
		return nil, nil, fmt.Errorf("read encrypted key: %w", err)
	}

	var nonceLen uint8
	if err := binary.Read(r, binary.BigEndian, &nonceLen); err != nil {
		return nil, nil, fmt.Errorf("read nonce length: %w", err)
	}
	if nonceLen > maxNonceLen {
		return nil, nil, fmt.Errorf("nonce length %d exceeds max %d", nonceLen, maxNonceLen)
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(r, nonce); err != nil {
		return nil, nil, fmt.Errorf("read nonce: %w", err)
	}

	var payloadLen uint64
	if err := binary.Read(r, binary.BigEndian, &payloadLen); err != nil {
		return nil, nil, fmt.Errorf("read payload length: %w", err)
	}
	if payloadLen > maxPayloadLen {
		return nil, nil, fmt.Errorf("payload length %d exceeds max %d", payloadLen, maxPayloadLen)
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, nil, fmt.Errorf("read payload: %w", err)
	}

	env = &Envelope{
		EncryptedFile: payload,
		EncryptedKey:  encKey,
		Nonce:         nonce,
	}
	return headerJSON, env, nil
}
