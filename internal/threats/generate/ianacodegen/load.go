package ianacodegen

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Data holds all IANA-derived sets for code generation.
type Data struct {
	MediaTypes []string
	Charsets   []string
	Protocols  []string
	IPFIXNames []string
}

// Load reads all CSVs from assetsDir and returns Data with the same semantics as
// cti-stix-validator/stix2validator/v21/enums.py (media_types, char_sets, protocols, ipfix).
func Load(assetsDir string) (*Data, error) {
	d := &Data{}

	if err := d.loadMediaTypes(assetsDir); err != nil {
		return nil, fmt.Errorf("media types: %w", err)
	}
	if err := d.loadCharsets(assetsDir); err != nil {
		return nil, fmt.Errorf("charsets: %w", err)
	}
	if err := d.loadProtocols(assetsDir); err != nil {
		return nil, fmt.Errorf("protocols: %w", err)
	}
	if err := d.loadIPFIX(assetsDir); err != nil {
		return nil, fmt.Errorf("ipfix: %w", err)
	}

	return d, nil
}

// loadMediaTypes: for each mediatype_<cat>.csv, use Template if set else cat/Name. Dedupe.
func (d *Data) loadMediaTypes(assetsDir string) error {
	glob := filepath.Join(assetsDir, "mediatype_*.csv")
	matches, err := filepath.Glob(glob)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{})
	var list []string
	for _, fpath := range matches {
		base := filepath.Base(fpath)
		// mediatype_application.csv -> application
		cat := strings.TrimSuffix(strings.TrimPrefix(base, "mediatype_"), ".csv")
		f, err := os.Open(fpath)
		if err != nil {
			return err
		}
		r := csv.NewReader(f)
		rows, err := r.ReadAll()
		f.Close()
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			continue
		}
		headers := rows[0]
		nameIdx := indexOf(headers, "Name")
		templateIdx := indexOf(headers, "Template")
		if nameIdx < 0 && templateIdx < 0 {
			continue
		}
		for _, row := range rows[1:] {
			var val string
			if templateIdx >= 0 && templateIdx < len(row) && strings.TrimSpace(row[templateIdx]) != "" {
				val = strings.TrimSpace(row[templateIdx])
			} else if nameIdx >= 0 && nameIdx < len(row) && strings.TrimSpace(row[nameIdx]) != "" {
				val = cat + "/" + strings.TrimSpace(row[nameIdx])
			}
			if val != "" {
				if _, ok := seen[val]; !ok {
					seen[val] = struct{}{}
					list = append(list, val)
				}
			}
		}
	}
	sort.Strings(list)
	d.MediaTypes = list
	return nil
}

// loadCharsets: Preferred MIME Name else Name. Dedupe.
func (d *Data) loadCharsets(assetsDir string) error {
	fpath := filepath.Join(assetsDir, "charsets.csv")
	f, err := os.Open(fpath)
	if err != nil {
		return err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.LazyQuotes = true
	rows, err := r.ReadAll()
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	headers := rows[0]
	prefIdx := indexOf(headers, "Preferred MIME Name")
	nameIdx := indexOf(headers, "Name")
	if prefIdx < 0 && nameIdx < 0 {
		return fmt.Errorf("charsets.csv: missing Preferred MIME Name and Name columns")
	}
	seen := make(map[string]struct{})
	var list []string
	for _, row := range rows[1:] {
		var val string
		if prefIdx >= 0 && prefIdx < len(row) && strings.TrimSpace(row[prefIdx]) != "" {
			val = strings.TrimSpace(row[prefIdx])
		} else if nameIdx >= 0 && nameIdx < len(row) && strings.TrimSpace(row[nameIdx]) != "" {
			val = strings.TrimSpace(row[nameIdx])
		}
		if val != "" {
			if _, ok := seen[val]; !ok {
				seen[val] = struct{}{}
				list = append(list, val)
			}
		}
	}
	sort.Strings(list)
	d.Charsets = list
	return nil
}

// loadProtocols: Service Name + Transport Protocol, then add ipv4, ipv6, ssl, tls, dns; sort.
func (d *Data) loadProtocols(assetsDir string) error {
	fpath := filepath.Join(assetsDir, "protocols.csv")
	f, err := os.Open(fpath)
	if err != nil {
		return err
	}
	defer f.Close()
	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		return err
	}
	set := make(map[string]struct{})
	for _, s := range []string{"ipv4", "ipv6", "ssl", "tls", "dns"} {
		set[s] = struct{}{}
	}
	if len(rows) > 0 {
		headers := rows[0]
		svcIdx := indexOf(headers, "Service Name")
		transIdx := indexOf(headers, "Transport Protocol")
		for _, row := range rows[1:] {
			if svcIdx >= 0 && svcIdx < len(row) {
				s := strings.TrimSpace(row[svcIdx])
				if s != "" {
					set[s] = struct{}{}
				}
			}
			if transIdx >= 0 && transIdx < len(row) {
				s := strings.TrimSpace(row[transIdx])
				if s != "" {
					set[s] = struct{}{}
				}
			}
		}
	}
	var list []string
	for k := range set {
		list = append(list, k)
	}
	sort.Strings(list)
	d.Protocols = list
	return nil
}

// loadIPFIX: non-empty Name column; sort for determinism.
func (d *Data) loadIPFIX(assetsDir string) error {
	fpath := filepath.Join(assetsDir, "ipfix-information-elements.csv")
	f, err := os.Open(fpath)
	if err != nil {
		return err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.LazyQuotes = true
	rows, err := r.ReadAll()
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	headers := rows[0]
	nameIdx := indexOf(headers, "Name")
	if nameIdx < 0 {
		return fmt.Errorf("ipfix-information-elements.csv: missing Name column")
	}
	seen := make(map[string]struct{})
	var list []string
	for _, row := range rows[1:] {
		if nameIdx < len(row) {
			val := strings.TrimSpace(row[nameIdx])
			if val != "" {
				if _, ok := seen[val]; !ok {
					seen[val] = struct{}{}
					list = append(list, val)
				}
			}
		}
	}
	sort.Strings(list)
	d.IPFIXNames = list
	return nil
}

func indexOf(h []string, name string) int {
	for i, s := range h {
		if s == name {
			return i
		}
	}
	return -1
}
