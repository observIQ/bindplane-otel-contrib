// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package textutils provides text encoding lookup and decoding helpers.
package textutils

import (
	"fmt"
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/encoding/unicode"
)

var encodingOverrides = map[string]encoding.Encoding{
	"utf-16":    unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM),
	"utf16":     unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM),
	"utf-8":     unicode.UTF8,
	"utf8":      unicode.UTF8,
	"utf-8-raw": UTF8Raw,
	"utf8-raw":  UTF8Raw,
	"ascii":     unicode.UTF8,
	"us-ascii":  unicode.UTF8,
	"nop":       encoding.Nop,
	"":          unicode.UTF8,
}

// LookupEncoding resolves an encoding name to a golang.org/x/text encoding.
func LookupEncoding(enc string) (encoding.Encoding, error) {
	if e, ok := encodingOverrides[strings.ToLower(enc)]; ok {
		return e, nil
	}
	e, err := ianaindex.IANA.Encoding(enc)
	if err != nil {
		return nil, fmt.Errorf("unsupported encoding '%s'", enc)
	}
	if e == nil {
		return nil, fmt.Errorf("no charmap defined for encoding '%s'", enc)
	}
	return e, nil
}

// IsNop reports whether the encoding name resolves to the no-op encoding.
func IsNop(enc string) bool {
	e, err := LookupEncoding(enc)
	if err != nil {
		return false
	}
	return e == encoding.Nop
}

// DecodeAsString converts the given encoded bytes using the given decoder. It returns the converted
// bytes or nil, err if any error occurred.
func DecodeAsString(decoder *encoding.Decoder, buf []byte) (string, error) {
	dstBuf, err := decoder.Bytes(buf)
	if err != nil {
		return "", err
	}
	return string(dstBuf), nil
}
