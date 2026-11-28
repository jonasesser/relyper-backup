package dbdump

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/garethgeorge/backrest/internal/relyper/discovery"
	"github.com/garethgeorge/backrest/internal/relyper/docker"
)

// ManifestName is written next to the dumps of an app and backed up with
// them. It holds no secrets.
const ManifestName = "relyper-dumps.json"

// Manifest describes the dumps of one app.
type Manifest struct {
	App       string    `json:"app"`
	CreatedAt time.Time `json:"created_at"`
	Dumps     []Dump    `json:"dumps"`
}

// Dump is one database dump.
type Dump struct {
	Service   string           `json:"service"`
	Engine    string           `json:"engine"`
	Image     string           `json:"image"`
	Container string           `json:"container"`
	File      string           `json:"file"`
	Bytes     int64            `json:"bytes"`
	SHA256    string           `json:"sha256"`
	User      string           `json:"user,omitempty"` // database superuser name (PostgreSQL), not secret
	Stats     map[string]int64 `json:"stats"`          // databases and their table/collection/key counts
	Duration  string           `json:"duration"`
}

// Dir is the dump directory of an app.
func Dir(base, appID string) string {
	return filepath.Join(base, discovery.Slug(appID))
}

// DumpApp dumps every database of the app into Dir(base, appID) and writes
// the manifest. Earlier dumps of the app are replaced only when all dumps
// succeeded.
func DumpApp(ctx context.Context, c *docker.Client, app *discovery.App, base string, log io.Writer) (*Manifest, error) {
	if log == nil {
		log = io.Discard
	}
	dir := Dir(base, app.ID)
	tmp := dir + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	m := &Manifest{App: app.ID, CreatedAt: time.Now().UTC()}
	for _, s := range app.Services {
		if s.Database == nil {
			continue
		}
		if s.State != "running" || s.ContainerID == "" {
			return nil, fmt.Errorf("%s: database container is not running on this host (%s)", s.Name, s.State)
		}
		d, err := dumpOne(ctx, c, s, tmp, log)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.Name, err)
		}
		m.Dumps = append(m.Dumps, *d)
	}
	if len(m.Dumps) == 0 {
		return nil, errors.New("the app has no database")
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, ManifestName), b, 0o600); err != nil {
		return nil, err
	}
	old := dir + ".old"
	_ = os.RemoveAll(old)
	if _, err := os.Stat(dir); err == nil {
		if err := os.Rename(dir, old); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		return nil, err
	}
	_ = os.RemoveAll(old)
	return m, nil
}

func fileName(service, ext string) string {
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, service)
	return safe + "." + ext
}

func dumpOne(ctx context.Context, c *docker.Client, s discovery.Service, dir string, log io.Writer) (*Dump, error) {
	e, ok := engines[s.Database.Engine]
	if !ok {
		return nil, fmt.Errorf("no dump support for %s", s.Database.Engine)
	}
	start := time.Now()
	fmt.Fprintf(log, "dumping %s (%s, container %s)\n", s.Name, s.Database.Engine, s.Container)
	// Counts first: they describe what the dump must contain.
	stats, err := runCapture(ctx, c, s.ContainerID, e.stats)
	if err != nil {
		return nil, fmt.Errorf("reading database statistics: %w", err)
	}
	d := &Dump{Service: s.Name, Engine: s.Database.Engine, Image: s.Image, Container: s.Container, File: fileName(s.Name, e.ext), Stats: parseStats(stats)}
	if s.Database.Engine == discovery.EnginePostgres {
		if u, err := runCapture(ctx, c, s.ContainerID, pgUserPrint); err == nil {
			d.User = strings.TrimSpace(u)
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, d.File), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	cw := &countWriter{w: io.MultiWriter(f, h)}
	var stderr bytes.Buffer
	res, err := c.Exec(ctx, s.ContainerID, []string{"sh", "-c", e.dump}, nil, "", cw, &limitedBuffer{buf: &stderr, max: 4096})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("dump exited with %d: %s", res.ExitCode, strings.TrimSpace(stderr.String()))
	}
	if cw.n == 0 {
		return nil, errors.New("dump is empty")
	}
	d.Bytes, d.SHA256, d.Duration = cw.n, hex.EncodeToString(h.Sum(nil)), time.Since(start).Round(time.Millisecond).String()
	fmt.Fprintf(log, "dumped %s: %d bytes, sha256 %s, %d databases\n", s.Name, d.Bytes, d.SHA256[:12], len(d.Stats))
	return d, nil
}

func runCapture(ctx context.Context, c *docker.Client, container, script string) (string, error) {
	var out, errb bytes.Buffer
	res, err := c.Exec(ctx, container, []string{"sh", "-c", script}, nil, "", &limitedBuffer{buf: &out, max: 1 << 20}, &limitedBuffer{buf: &errb, max: 4096})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return out.String(), fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

type limitedBuffer struct {
	buf *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}

// VerifyResult is the outcome of restoring one dump into a throwaway
// container.
type VerifyResult struct {
	Service  string           `json:"service"`
	Engine   string           `json:"engine"`
	Image    string           `json:"image"`
	OK       bool             `json:"ok"`
	Problems []string         `json:"problems,omitempty"`
	Expected map[string]int64 `json:"expected"`
	Restored map[string]int64 `json:"restored"`
	Duration string           `json:"duration"`
}

// Verify restores a dump file into an isolated container (same image, no
// network, label relyper.verify=true), compares the content with the counts
// recorded at dump time and removes the container.
func Verify(ctx context.Context, c *docker.Client, d Dump, dumpPath string, log io.Writer) (*VerifyResult, error) {
	if log == nil {
		log = io.Discard
	}
	e, ok := engines[d.Engine]
	if !ok {
		return nil, fmt.Errorf("no verification support for %s", d.Engine)
	}
	start := time.Now()
	r := &VerifyResult{Service: d.Service, Engine: d.Engine, Image: d.Image, Expected: d.Stats}
	fi, err := os.Stat(dumpPath)
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	if d.SHA256 != "" {
		sum, err := fileSHA256(dumpPath)
		if err != nil {
			return nil, err
		}
		if sum != d.SHA256 {
			r.Problems = append(r.Problems, "checksum of the dump does not match the manifest")
			r.Duration = time.Since(start).Round(time.Millisecond).String()
			return r, nil
		}
	}
	pw := randomHex(VerifyPasswordLen / 2)
	name := "relyper-verify-" + randomHex(6)
	id, err := c.CreateVerifyContainer(ctx, name, docker.ContainerConfig{Image: d.Image, Env: e.verifyEnv(pw, d.User)})
	if err != nil {
		return nil, fmt.Errorf("creating verification container from %s: %w", d.Image, err)
	}
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.RemoveVerifyContainer(rmCtx, id); err != nil {
			fmt.Fprintf(log, "removing %s failed: %v\n", name, err)
		}
	}()
	fmt.Fprintf(log, "verification container %s from %s\n", name, d.Image)
	restoreName := "dump." + e.ext
	if e.restoreBeforeStart != "" {
		restoreName = "dump.rdb"
		if err := c.PutArchive(ctx, id, e.restoreBeforeStart, tarOf(restoreName, dumpPath, size)); err != nil {
			return nil, fmt.Errorf("copying the dump: %w", err)
		}
	}
	if err := c.StartContainer(ctx, id); err != nil {
		return nil, fmt.Errorf("starting verification container: %w", err)
	}
	if err := waitReady(ctx, c, id, e.ready, pw, 3*time.Minute); err != nil {
		return nil, err
	}
	if e.restoreBeforeStart == "" {
		if _, err := runCapture(ctx, c, id, prepareRestoreDir); err != nil {
			return nil, err
		}
		if err := c.PutArchive(ctx, id, restoreDir, tarOf(restoreName, dumpPath, size)); err != nil {
			return nil, fmt.Errorf("copying the dump: %w", err)
		}
		if out, err := runCaptureEnv(ctx, c, id, e.restore, verifyExecEnv(d.Engine, pw)); err != nil {
			r.Problems = append(r.Problems, "restore failed: "+firstLine(err.Error()+" "+out))
		}
	}
	stats, err := runCaptureEnv(ctx, c, id, e.stats, verifyExecEnv(d.Engine, pw))
	if err != nil {
		r.Problems = append(r.Problems, "reading restored content failed: "+firstLine(err.Error()))
	}
	r.Restored = parseStats(stats)
	r.Problems = append(r.Problems, compareStats(d.Stats, r.Restored)...)
	r.OK = len(r.Problems) == 0
	r.Duration = time.Since(start).Round(time.Millisecond).String()
	fmt.Fprintf(log, "verification of %s: ok=%v %v\n", d.Service, r.OK, r.Problems)
	return r, nil
}

// verifyExecEnv gives exec'd commands in the verification container the
// random password (the container env already has it; this keeps scripts
// uniform).
func verifyExecEnv(engine, pw string) []string {
	switch engine {
	case discovery.EnginePostgres:
		return []string{"POSTGRES_PASSWORD=" + pw}
	case discovery.EngineMySQL, discovery.EngineMariaDB:
		return []string{"MARIADB_ROOT_PASSWORD=" + pw}
	}
	return nil
}

func runCaptureEnv(ctx context.Context, c *docker.Client, container, script string, env []string) (string, error) {
	var out, errb bytes.Buffer
	res, err := c.Exec(ctx, container, []string{"sh", "-c", script}, env, "", &limitedBuffer{buf: &out, max: 1 << 20}, &limitedBuffer{buf: &errb, max: 4096})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return out.String(), fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func waitReady(ctx context.Context, c *docker.Client, id, script, pw string, max time.Duration) error {
	deadline := time.Now().Add(max)
	for {
		res, err := c.Exec(ctx, id, []string{"sh", "-c", script}, []string{"MARIADB_ROOT_PASSWORD=" + pw}, "", nil, nil)
		if err == nil && res.ExitCode == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("verification container did not become ready")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// tarOf streams a one-file tar archive of the file at path.
func tarOf(name, path string, size int64) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		f, err := os.Open(path)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		defer f.Close()
		tw := tar.NewWriter(pw)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: size, ModTime: time.Now(), Uid: 999, Gid: 999}); err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := io.CopyN(tw, f, size); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.CloseWithError(tw.Close())
	}()
	return pr
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
