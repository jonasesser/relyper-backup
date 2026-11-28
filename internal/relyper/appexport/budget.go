// Package appexport is Relyper-owned: backups of single customers of an app
// that keeps all its customers in one place, starting with Relyper Budget.
//
// Relyper Budget stores every user's budgets end-to-end encrypted. Its
// backup interface (/relyper-backup/v1/, relyper-budget #298) exports all
// budgets of one user as a tar (budget files, sync data, documents, still
// encrypted, manifest with SHA-256) and imports such a tar as new budgets of
// that user. Relyper Backup calls the export in the "before backup" hook of
// the customer's plan (cmd/relyper-appexport) and keeps the tar in the
// customer's repository; a restore dumps the tar from a snapshot and hands
// it to the import. Nothing is decrypted on the way.
package appexport

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// AppBudget is the app name used in plans and links.
const AppBudget = "budget"

// ExportFile is the name of the export inside the export directory.
const ExportFile = "budget-export.tar"

const (
	exportFormat  = "relyper-budget-export"
	exportVersion = 1
	minToken      = 32
)

var userIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// ValidUserID reports whether id can be a Relyper Budget user ID.
func ValidUserID(id string) bool { return userIDPattern.MatchString(id) }

// BudgetConfig is read from RELYPER_BUDGET_URL and RELYPER_BUDGET_TOKEN(_FILE).
type BudgetConfig struct {
	URL   string
	Token string
}

// BudgetConfigFromEnv returns nil, nil when RELYPER_BUDGET_URL is not set.
func BudgetConfigFromEnv(getenv func(string) string) (*BudgetConfig, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	raw := strings.TrimRight(strings.TrimSpace(getenv("RELYPER_BUDGET_URL")), "/")
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return nil, errors.New("RELYPER_BUDGET_URL must be an http(s) URL without user info, e.g. http://relyper-budget:80")
	}
	token := strings.TrimSpace(getenv("RELYPER_BUDGET_TOKEN"))
	if f := strings.TrimSpace(getenv("RELYPER_BUDGET_TOKEN_FILE")); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("RELYPER_BUDGET_TOKEN_FILE: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	if len(token) < minToken {
		return nil, fmt.Errorf("RELYPER_BUDGET_TOKEN must have at least %d characters (the same value as RELYPER_BACKUP_TOKEN in Relyper Budget)", minToken)
	}
	return &BudgetConfig{URL: raw, Token: token}, nil
}

// BudgetUser is a user of Relyper Budget with the budgets they own.
type BudgetUser struct {
	ID          string          `json:"id"`
	UserName    string          `json:"userName"`
	DisplayName string          `json:"displayName"`
	Enabled     bool            `json:"enabled"`
	Budgets     []BudgetSummary `json:"budgets"`
}

// BudgetSummary is one budget without its content.
type BudgetSummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// Created is a budget added by an import.
type Created struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	From string `json:"from"`
}

// Manifest is the last entry of an export.
type Manifest struct {
	Format    string `json:"format"`
	Version   int    `json:"version"`
	CreatedAt string `json:"createdAt"`
	User      struct {
		ID       string `json:"id"`
		UserName string `json:"userName"`
	} `json:"user"`
	Budgets []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Documents int    `json:"documents"`
	} `json:"budgets"`
	Entries []struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	} `json:"entries"`
}

// Budget talks to the backup interface of Relyper Budget.
type Budget struct {
	Cfg    BudgetConfig
	Client *http.Client
}

func (b *Budget) client() *http.Client {
	if b.Client != nil {
		return b.Client
	}
	return &http.Client{Timeout: 2 * time.Hour}
}

func (b *Budget) request(ctx context.Context, method, path string, q url.Values, body io.Reader) (*http.Response, error) {
	u := b.Cfg.URL + "/relyper-backup/v1/" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.Cfg.Token)
	req.Header.Set("User-Agent", "relyper-backup")
	if body != nil {
		req.Header.Set("Content-Type", "application/x-tar")
	}
	resp, err := b.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("Relyper Budget is not reachable: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		var e struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
		switch resp.StatusCode {
		case http.StatusNotFound:
			if e.Reason == "unknown-user" {
				return nil, errors.New("Relyper Budget does not know this user")
			}
			return nil, errors.New("the backup interface of Relyper Budget is off (set RELYPER_BACKUP_TOKEN there)")
		case http.StatusUnauthorized:
			return nil, errors.New("Relyper Budget refused the token (RELYPER_BUDGET_TOKEN must equal RELYPER_BACKUP_TOKEN there)")
		case http.StatusTooManyRequests:
			return nil, errors.New("Relyper Budget is busy with another backup or restore, try again in a moment")
		}
		if e.Reason != "" {
			return nil, fmt.Errorf("Relyper Budget: %s", e.Reason)
		}
		return nil, fmt.Errorf("Relyper Budget answered %s", resp.Status)
	}
	return resp, nil
}

// Users lists the users of Relyper Budget.
func (b *Budget) Users(ctx context.Context) ([]BudgetUser, error) {
	resp, err := b.request(ctx, http.MethodGet, "users", nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data []BudgetUser `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("unexpected answer from Relyper Budget: %w", err)
	}
	return out.Data, nil
}

// Export writes the export of one user to dir/ExportFile. The file is only
// replaced when the new export is complete and every checksum matches, so a
// failed export never leaves a broken file for the backup.
func (b *Budget) Export(ctx context.Context, userID, dir string) (*Manifest, error) {
	if !ValidUserID(userID) {
		return nil, errors.New("invalid user ID")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	resp, err := b.request(ctx, http.MethodGet, "export", url.Values{"user": {userID}}, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(dir, ".export-*.tar")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("export cut off: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		return nil, err
	}
	m, err := Verify(tmp)
	tmp.Close()
	if err != nil {
		return nil, err
	}
	if m.User.ID != userID {
		return nil, errors.New("the export belongs to another user")
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, ExportFile)); err != nil {
		return nil, err
	}
	return m, nil
}

// Verify reads a whole export and checks it against its manifest.
func Verify(r io.Reader) (*Manifest, error) {
	tr := tar.NewReader(r)
	got := map[string][2]string{} // path -> size, sha256
	var manifest []byte
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("the export is not a valid tar: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("unexpected entry %q", h.Name)
		}
		if h.Name == "manifest.json" {
			if manifest, err = io.ReadAll(io.LimitReader(tr, 64<<20)); err != nil {
				return nil, err
			}
			continue
		}
		hash := sha256.New()
		n, err := io.Copy(hash, tr)
		if err != nil {
			return nil, err
		}
		got[h.Name] = [2]string{fmt.Sprint(n), hex.EncodeToString(hash.Sum(nil))}
	}
	if manifest == nil {
		return nil, errors.New("the export has no manifest (cut off?)")
	}
	var m Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	if m.Format != exportFormat || m.Version != exportVersion {
		return nil, fmt.Errorf("unknown export format %q version %d", m.Format, m.Version)
	}
	if len(m.Entries) != len(got) {
		return nil, errors.New("the export does not match its manifest")
	}
	for _, e := range m.Entries {
		g, ok := got[e.Path]
		if !ok || g[0] != fmt.Sprint(e.Size) || g[1] != e.SHA256 {
			return nil, fmt.Errorf("checksum mismatch for %s", e.Path)
		}
	}
	return &m, nil
}

// Import hands an export to Relyper Budget, which adds its budgets as new
// budgets of the user.
func (b *Budget) Import(ctx context.Context, userID string, export io.Reader) ([]Created, error) {
	if !ValidUserID(userID) {
		return nil, errors.New("invalid user ID")
	}
	resp, err := b.request(ctx, http.MethodPost, "import", url.Values{"user": {userID}}, export)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			Budgets []Created `json:"budgets"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("unexpected answer from Relyper Budget: %w", err)
	}
	return out.Data.Budgets, nil
}

// Dir is the export directory of one customer user below base.
func Dir(base, tenantID, userID string) string {
	return filepath.Join(base, tenantID, "budget-"+userID)
}

// HookCommand is the "before backup" hook of a budget plan. hookpolicy
// accepts exactly this form.
func HookCommand(command, dir, userID string) (string, error) {
	if strings.ContainsAny(dir, `'"\$`+"` ") || !ValidUserID(userID) {
		return "", errors.New("the export directory or user ID contains characters that are not allowed")
	}
	return fmt.Sprintf("%s budget --dir '%s' '%s'", command, dir, userID), nil
}
