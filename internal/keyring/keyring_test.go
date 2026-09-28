package keyring

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadKeepsOnlyVersionedRawKeys(t *testing.T) {
	dir := t.TempDir()
	oldKey := bytes.Repeat([]byte{0x42}, 32)
	newKey := bytes.Repeat([]byte{0x73}, 32)
	if err := os.WriteFile(filepath.Join(dir, "external-shop-v1.key"), oldKey, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "external-shop-v2.key"), newKey, 0600); err != nil {
		t.Fatal(err)
	}
	keys, err := Load(dir)
	if err != nil || len(keys) != 2 || !bytes.Equal(keys["external-shop-v1"], oldKey) || !bytes.Equal(keys["external-shop-v2"], newKey) {
		t.Fatalf("versioned keys not loaded: count=%d err=%v", len(keys), err)
	}
	if _, err := Load(""); err != nil {
		t.Fatalf("empty key directory should disable external keys: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "external-bad-v1.key"), []byte(strings.Repeat("x", 31)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || strings.Contains(err.Error(), string(oldKey)) {
		t.Fatalf("short key was accepted or leaked: %v", err)
	}
}
