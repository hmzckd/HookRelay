package keyring

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var keyIDPattern = regexp.MustCompile(`^external-[a-z0-9][a-z0-9-]{0,55}-v[1-9][0-9]{0,3}$`)

func ValidID(id string) bool { return keyIDPattern.MatchString(id) }

// Load reads fixed-size raw key files. Files are named <key-id>.key and stay
// outside source control; only the IDs are stored in PostgreSQL.
func Load(dir string) (map[string][]byte, error) {
	keys := map[string][]byte{}
	if dir == "" {
		return keys, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, errors.New("external key directory is unavailable")
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".key") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".key")
		if !ValidID(id) || !entry.Type().IsRegular() || len(keys) >= 64 {
			return nil, errors.New("external key directory contains an invalid key file")
		}
		info, err := entry.Info()
		if err != nil || info.Size() < 32 || info.Size() > 128 {
			return nil, errors.New("external key file must contain 32–128 raw bytes")
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("external key file must be readable only by its owner")
		}
		value, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil || len(value) < 32 || len(value) > 128 {
			return nil, errors.New("external key file could not be read")
		}
		keys[id] = value
	}
	return keys, nil
}
