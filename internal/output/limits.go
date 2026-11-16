package output

import "fmt"

type Limits struct {
	MaxManifestBytes int
	MaxManifestFiles int
	MaxFileBytes     int64
	MaxOutputBytes   int64
}

func (l Limits) WithDefaults() Limits {
	if l.MaxManifestBytes == 0 {
		l.MaxManifestBytes = 262144
	}
	if l.MaxManifestFiles == 0 {
		l.MaxManifestFiles = DefaultMaxManifestFiles
	}
	if l.MaxFileBytes == 0 {
		l.MaxFileBytes = DefaultMaxFileBytes
	}
	if l.MaxOutputBytes == 0 {
		l.MaxOutputBytes = DefaultMaxOutputBytes
	}
	return l
}
func (l Limits) Validate() error {
	if l.MaxManifestBytes <= 0 || l.MaxManifestFiles <= 0 || l.MaxFileBytes <= 0 || l.MaxOutputBytes <= 0 || l.MaxFileBytes > l.MaxOutputBytes {
		return fmt.Errorf("invalid output limits")
	}
	return nil
}
func (l Limits) Parse(raw []byte, prefix string) (Manifest, error) {
	if err := l.Validate(); err != nil {
		return Manifest{}, err
	}
	m, err := ParseManifest(raw, l.MaxManifestBytes, l.MaxManifestFiles, l.MaxFileBytes, l.MaxOutputBytes)
	if err != nil {
		return Manifest{}, err
	}
	return m, ValidateManifestPrefix(m, prefix)
}
