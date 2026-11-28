// Relyper-owned tests of the Relyper Budget export client.
package appexport

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "0123456789abcdef0123456789abcdef"

func buildExport(t *testing.T, user string, files map[string]string, tamper bool) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	type entry struct {
		Path   string `json:"path"`
		Size   int    `json:"size"`
		SHA256 string `json:"sha256"`
	}
	var entries []entry
	for name, content := range files {
		sum := sha256.Sum256([]byte(content))
		entries = append(entries, entry{name, len(content), hex.EncodeToString(sum[:])})
		if tamper {
			content = strings.ToUpper(content)
		}
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg})
		tw.Write([]byte(content))
	}
	m, _ := json.Marshal(map[string]any{"format": "relyper-budget-export", "version": 1, "user": map[string]string{"id": user},
		"budgets": []map[string]any{{"id": "b1", "name": "Haushalt", "documents": 1}}, "entries": entries})
	tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(m)), Typeflag: tar.TypeReg})
	tw.Write(m)
	tw.Close()
	return buf.Bytes()
}

type fakeServer struct {
	export   []byte
	imported []byte
}

func (f *fakeServer) start(t *testing.T) *Budget {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/relyper-backup/v1/users":
			w.Write([]byte(`{"status":"ok","data":[{"id":"u1","userName":"kunde@example.com","budgets":[{"id":"b1","name":"Haushalt","bytes":12}]}]}`))
		case "/relyper-backup/v1/export":
			if r.URL.Query().Get("user") != "u1" {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"status":"error","reason":"unknown-user"}`))
				return
			}
			w.Write(f.export)
		case "/relyper-backup/v1/import":
			f.imported, _ = io.ReadAll(r.Body)
			w.Write([]byte(`{"status":"ok","data":{"budgets":[{"id":"n1","name":"Haushalt (restored 2026-11-28)","from":"b1"}]}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return &Budget{Cfg: BudgetConfig{URL: srv.URL, Token: testToken}}
}

func TestExportVerifiesAndReplacesAtomically(t *testing.T) {
	files := map[string]string{"budgets/b1/file.blob": "cipher", "budgets/b1/documents/d1.blob": "doc"}
	f := &fakeServer{export: buildExport(t, "u1", files, false)}
	b := f.start(t)
	dir := t.TempDir()
	m, err := b.Export(context.Background(), "u1", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Budgets) != 1 || len(m.Entries) != 2 {
		t.Fatalf("manifest %+v", m)
	}
	good, _ := os.ReadFile(filepath.Join(dir, ExportFile))

	// A tampered export fails and leaves the last good one in place.
	f.export = buildExport(t, "u1", files, true)
	if _, err := b.Export(context.Background(), "u1", dir); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered export: %v", err)
	}
	// A cut-off export (no manifest) fails as well.
	f.export = good[:512]
	if _, err := b.Export(context.Background(), "u1", dir); err == nil {
		t.Fatal("cut-off export accepted")
	}
	// An export of another user is refused.
	f.export = buildExport(t, "u2", files, false)
	if _, err := b.Export(context.Background(), "u1", dir); err == nil {
		t.Fatal("export of another user accepted")
	}
	now, _ := os.ReadFile(filepath.Join(dir, ExportFile))
	if !bytes.Equal(now, good) {
		t.Fatal("a failed export replaced the last good one")
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("temp files left: %v", ents)
	}
	if _, err := b.Export(context.Background(), "nobody", dir); err == nil || !strings.Contains(err.Error(), "does not know") {
		t.Fatalf("unknown user: %v", err)
	}
}

func TestUsersAndImport(t *testing.T) {
	f := &fakeServer{}
	b := f.start(t)
	users, err := b.Users(context.Background())
	if err != nil || len(users) != 1 || users[0].UserName != "kunde@example.com" {
		t.Fatalf("users %+v %v", users, err)
	}
	created, err := b.Import(context.Background(), "u1", strings.NewReader("tar"))
	if err != nil || len(created) != 1 || string(f.imported) != "tar" {
		t.Fatalf("import %+v %v %q", created, err, f.imported)
	}
	bad := &Budget{Cfg: BudgetConfig{URL: b.Cfg.URL, Token: strings.Repeat("x", 32)}}
	if _, err := bad.Users(context.Background()); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("wrong token: %v", err)
	}
}

func TestConfigAndHook(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if c, err := BudgetConfigFromEnv(env(nil)); c != nil || err != nil {
		t.Fatal("off without URL")
	}
	if _, err := BudgetConfigFromEnv(env(map[string]string{"RELYPER_BUDGET_URL": "http://budget", "RELYPER_BUDGET_TOKEN": "short"})); err == nil {
		t.Fatal("short token accepted")
	}
	if _, err := BudgetConfigFromEnv(env(map[string]string{"RELYPER_BUDGET_URL": "ftp://budget", "RELYPER_BUDGET_TOKEN": testToken})); err == nil {
		t.Fatal("ftp accepted")
	}
	if c, err := BudgetConfigFromEnv(env(map[string]string{"RELYPER_BUDGET_URL": "http://relyper-budget:80/", "RELYPER_BUDGET_TOKEN": testToken})); err != nil || c.URL != "http://relyper-budget:80" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := HookCommand("relyper-appexport", "/data/x y", "u1"); err == nil {
		t.Fatal("space in dir accepted")
	}
	if _, err := HookCommand("relyper-appexport", "/data/x", "u1'; id"); err == nil {
		t.Fatal("quote in user accepted")
	}
}
