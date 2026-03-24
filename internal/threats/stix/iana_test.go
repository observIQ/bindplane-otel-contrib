package stix

import "testing"

// Golden: known values that must be present in the generated IANA sets (parity with Python enums).
var (
	goldenMIMEType  = "application/zip"
	goldenCharset   = "UTF-8"
	goldenProtocol  = "tcp"
	goldenIPFIXName = "octetDeltaCount"
)

func TestIsValidMIMEType_Golden(t *testing.T) {
	if !IsValidMIMEType(goldenMIMEType) {
		t.Errorf("IsValidMIMEType(%q) = false, want true (golden)", goldenMIMEType)
	}
}

func TestIsValidMIMEType_Invalid(t *testing.T) {
	if IsValidMIMEType("invalid/mime") {
		t.Error("IsValidMIMEType(\"invalid/mime\") = true, want false")
	}
}

func TestIsValidMIMEType_Empty(t *testing.T) {
	if IsValidMIMEType("") {
		t.Error("IsValidMIMEType(\"\") = true, want false")
	}
}

func TestIsValidCharset_Golden(t *testing.T) {
	if !IsValidCharset(goldenCharset) {
		t.Errorf("IsValidCharset(%q) = false, want true (golden)", goldenCharset)
	}
}

func TestIsValidCharset_Invalid(t *testing.T) {
	if IsValidCharset("INVALID-CHARSET") {
		t.Error("IsValidCharset(\"INVALID-CHARSET\") = true, want false")
	}
}

func TestIsValidCharset_Empty(t *testing.T) {
	if IsValidCharset("") {
		t.Error("IsValidCharset(\"\") = true, want false")
	}
}

func TestIsValidProtocol_Golden(t *testing.T) {
	if !IsValidProtocol(goldenProtocol) {
		t.Errorf("IsValidProtocol(%q) = false, want true (golden)", goldenProtocol)
	}
}

func TestIsValidProtocol_Extra(t *testing.T) {
	// ipv4, ipv6, ssl, tls, dns are hardcoded in codegen
	for _, p := range []string{"ipv4", "ipv6", "ssl", "tls", "dns"} {
		if !IsValidProtocol(p) {
			t.Errorf("IsValidProtocol(%q) = false, want true", p)
		}
	}
}

func TestIsValidProtocol_Invalid(t *testing.T) {
	if IsValidProtocol("not-a-protocol") {
		t.Error("IsValidProtocol(\"not-a-protocol\") = true, want false")
	}
}

func TestIsValidProtocol_Empty(t *testing.T) {
	if IsValidProtocol("") {
		t.Error("IsValidProtocol(\"\") = true, want false")
	}
}

func TestIsValidIPFIXName_Golden(t *testing.T) {
	if !IsValidIPFIXName(goldenIPFIXName) {
		t.Errorf("IsValidIPFIXName(%q) = false, want true (golden)", goldenIPFIXName)
	}
}

func TestIsValidIPFIXName_Invalid(t *testing.T) {
	if IsValidIPFIXName("notAnIPFIXName") {
		t.Error("IsValidIPFIXName(\"notAnIPFIXName\") = true, want false")
	}
}

func TestIsValidIPFIXName_Empty(t *testing.T) {
	if IsValidIPFIXName("") {
		t.Error("IsValidIPFIXName(\"\") = true, want false")
	}
}
