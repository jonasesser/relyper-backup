// Relyper-owned test of the escrow tool.
package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/garethgeorge/backrest/internal/relyper/portal"
)

func TestKeygenAndOpen(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "escrow.key")
	if err := run([]string{"keygen", keyFile}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"keygen", keyFile}); err == nil {
		t.Fatal("existing private key overwritten")
	}
	if st, _ := os.Stat(keyFile); st.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v", st.Mode())
	}
	priv, pub, err := readKey(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := portal.ParseEscrowPublicKey(base64.StdEncoding.EncodeToString(pub[:]))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := portal.Seal(parsed, portal.EscrowRecord{Tenant: "acme", Password: "pw-123"})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := portal.Open(pub, priv, sealed)
	if err != nil || rec.Password != "pw-123" {
		t.Fatalf("%v %+v", err, rec)
	}
	f := filepath.Join(dir, "x.sealed")
	os.WriteFile(f, sealed, 0o600)
	if err := run([]string{"open", keyFile, f}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"open", keyFile, filepath.Join(dir, "missing")}); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("missing file: %v", err)
	}
}
