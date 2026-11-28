// Package infra is Relyper-owned: application-aware backup on a Docker host.
// It ties discovery, database dumps, Backrest plans and the operation log
// together:
//
//   - coverage: which apps, volumes and databases are protected by a
//     completed backup, based on indexed snapshots and their paths, not on
//     configuration alone;
//   - app plans: one backup plan per app (its data paths plus the dumps of
//     its databases, made by a hook right before the backup);
//   - verification: database dumps restored from the latest snapshot into
//     throwaway containers, and sample files restored from snapshots;
//   - readiness and a disaster recovery report;
//   - optional periodic verification and notifications.
package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/config"
	"github.com/garethgeorge/backrest/internal/env"
	"github.com/garethgeorge/backrest/internal/oplog"
	"github.com/garethgeorge/backrest/internal/orchestrator"
	"github.com/garethgeorge/backrest/internal/relyper/audit"
	"github.com/garethgeorge/backrest/internal/relyper/discovery"
	"github.com/garethgeorge/backrest/internal/relyper/docker"
	"go.uber.org/zap"
)

// Deps are the Backrest components used.
type Deps struct {
	Config       config.ConfigStore
	Orchestrator *orchestrator.Orchestrator
	OpLog        *oplog.OpLog
	Audit        *audit.Log
	// DataDir is <data>/relyper.
	DataDir string
}

// Settings come from environment variables.
type Settings struct {
	DumpDir           string
	CoverageMaxAge    time.Duration
	VerifyInterval    time.Duration // 0 = no periodic verification
	NotifyURLs        []string
	DumpCommand       string
	PathMap           []discovery.PathMapping
	DockerEndpoint    string
	DockerConfigError string
	// NotifySuccess also reports successful backups (RELYPER_NOTIFY_SUCCESS).
	NotifySuccess bool
	// LowStoragePercent: notify when less free space is left (default 10).
	LowStoragePercent int
	// KnownHostsPath is Relyper Backup's SSH known_hosts (SFTP free space).
	KnownHostsPath string
	// RestoreDir is where guided restores stage data (RELYPER_RESTORE_DIR;
	// default /restore if it exists, else <data>/relyper/restore).
	RestoreDir string
	// TagsPath is the file with the Relyper Backup tags of apps (in the
	// config directory, so a backup of /config keeps them).
	TagsPath string
}

// SettingsFromEnv reads RELYPER_* variables.
func SettingsFromEnv(dataDir string) Settings {
	s := Settings{
		DumpDir:           filepath.Join(dataDir, "dumps"),
		CoverageMaxAge:    48 * time.Hour,
		VerifyInterval:    7 * 24 * time.Hour,
		DumpCommand:       "relyper-dbdump",
		LowStoragePercent: 10,
		DockerEndpoint:    docker.EndpointFromEnv(),
	}
	if v := os.Getenv("RELYPER_DUMP_DIR"); v != "" && filepath.IsAbs(v) {
		s.DumpDir = filepath.Clean(v)
	}
	if n, err := strconv.Atoi(os.Getenv("RELYPER_COVERAGE_MAX_AGE_HOURS")); err == nil && n > 0 {
		s.CoverageMaxAge = time.Duration(n) * time.Hour
	}
	if v, ok := os.LookupEnv("RELYPER_VERIFY_INTERVAL_DAYS"); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			s.VerifyInterval = time.Duration(n) * 24 * time.Hour
		}
	}
	s.NotifyURLs = strings.Fields(os.Getenv("RELYPER_NOTIFY_URLS"))
	s.NotifySuccess, _ = strconv.ParseBool(os.Getenv("RELYPER_NOTIFY_SUCCESS"))
	if n, err := strconv.Atoi(os.Getenv("RELYPER_LOW_STORAGE_PERCENT")); err == nil && n >= 0 && n < 100 {
		s.LowStoragePercent = n
	}
	if v := os.Getenv("RELYPER_RESTORE_DIR"); v != "" && filepath.IsAbs(v) {
		s.RestoreDir = filepath.Clean(v)
	}
	s.TagsPath = discovery.TagsPath(env.ConfigFilePath())
	if v := os.Getenv("RELYPER_DUMP_COMMAND"); v != "" {
		s.DumpCommand = v
	}
	pm, err := discovery.PathMapFromEnv()
	if err != nil {
		s.DockerConfigError = err.Error()
	}
	s.PathMap = pm
	return s
}

// Service is the entry point for the web UI and MCP.
type Service struct {
	d      Deps
	s      Settings
	docker *docker.Client
	store  *store

	mu       sync.Mutex
	inv      *discovery.Inventory
	invAt    time.Time
	busy     map[string]bool // app or plan IDs with a running verification
	lastNote map[string]time.Time
	notified map[string]string // app ID -> coverage state last notified
	storage  map[string]StorageInfo
}

// New creates the service. Docker is optional: without it, discovery reports
// the error and everything else keeps working.
func New(d Deps, s Settings) (*Service, error) {
	st, err := openStore(filepath.Join(d.DataDir, "infra-state.json"))
	if err != nil {
		return nil, err
	}
	svc := &Service{d: d, s: s, store: st, busy: map[string]bool{}, lastNote: map[string]time.Time{}, notified: map[string]string{}, storage: map[string]StorageInfo{}}
	if c, err := docker.New(s.DockerEndpoint); err == nil {
		svc.docker = c
	} else {
		svc.s.DockerConfigError = err.Error()
	}
	return svc, nil
}

// Settings returns the effective settings.
func (s *Service) Settings() Settings { return s.s }

const inventoryTTL = 20 * time.Second

// Inventory discovers the Docker host (cached for a few seconds).
func (s *Service) Inventory(ctx context.Context, refresh bool) *discovery.Inventory {
	s.mu.Lock()
	if !refresh && s.inv != nil && time.Since(s.invAt) < inventoryTTL {
		inv := s.inv
		s.mu.Unlock()
		return inv
	}
	s.mu.Unlock()
	var inv *discovery.Inventory
	if s.docker == nil {
		inv = &discovery.Inventory{GeneratedAt: time.Now(), Docker: discovery.Host{Endpoint: s.s.DockerEndpoint, Error: s.s.DockerConfigError}}
	} else {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		inv = discovery.Discover(ctx, s.docker, discovery.Options{PathMap: s.s.PathMap, SelfID: discovery.SelfContainerID()})
		if s.s.DockerConfigError != "" && inv.Docker.Error == "" {
			inv.Docker.Error = s.s.DockerConfigError
		}
		if s.s.TagsPath != "" {
			if tags, err := discovery.LoadTags(s.s.TagsPath); err == nil {
				inv.ApplyTags(tags)
			}
		}
	}
	s.mu.Lock()
	s.inv, s.invAt = inv, time.Now()
	s.mu.Unlock()
	return inv
}

func (s *Service) cfg() (*v1.Config, error) {
	return s.d.Config.Get()
}

func (s *Service) record(actor, action, outcome, msg string) {
	if s.d.Audit != nil {
		s.d.Audit.Record(audit.Event{Actor: actor, Source: "infra", Action: action, Outcome: outcome, Message: msg})
	}
}

// lock marks key busy; it returns false if a run is already going on.
func (s *Service) lock(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[key] {
		return false
	}
	s.busy[key] = true
	return true
}

func (s *Service) unlock(key string) {
	s.mu.Lock()
	delete(s.busy, key)
	s.mu.Unlock()
}

// ---- persisted results ----

type state struct {
	Verifications map[string][]VerifyRecord      `json:"verifications"` // by app ID
	RestoreTests  map[string][]RestoreTestRecord `json:"restore_tests"` // by plan ID
}

type store struct {
	path string
	mu   sync.Mutex
	st   state
}

const keepRecords = 20

func openStore(path string) (*store, error) {
	s := &store{path: path, st: state{Verifications: map[string][]VerifyRecord{}, RestoreTests: map[string][]RestoreTestRecord{}}}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &s.st); err != nil {
			zap.S().Warnw("ignoring unreadable infra state", "path", path, "error", err)
		}
		if s.st.Verifications == nil {
			s.st.Verifications = map[string][]VerifyRecord{}
		}
		if s.st.RestoreTests == nil {
			s.st.RestoreTests = map[string][]RestoreTestRecord{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func (s *store) saveLocked() error {
	b, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *store) addVerification(r VerifyRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := append([]VerifyRecord{r}, s.st.Verifications[r.App]...)
	if len(list) > keepRecords {
		list = list[:keepRecords]
	}
	s.st.Verifications[r.App] = list
	return s.saveLocked()
}

func (s *store) addRestoreTest(r RestoreTestRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := append([]RestoreTestRecord{r}, s.st.RestoreTests[r.Plan]...)
	if len(list) > keepRecords {
		list = list[:keepRecords]
	}
	s.st.RestoreTests[r.Plan] = list
	return s.saveLocked()
}

func (s *store) verifications(app string) []VerifyRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]VerifyRecord(nil), s.st.Verifications[app]...)
}

func (s *store) restoreTests(plan string) []RestoreTestRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RestoreTestRecord(nil), s.st.RestoreTests[plan]...)
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprint(err)
}

// lastVerified returns the newest successful database and file restore
// tests across all apps and plans.
func (s *store) lastVerified() (db, files *VerifiedRestore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for app, list := range s.st.Verifications {
		for _, r := range list {
			if r.OK && (db == nil || r.Time > db.Time) {
				db = &VerifiedRestore{Subject: app, Time: r.Time}
			}
		}
	}
	for plan, list := range s.st.RestoreTests {
		for _, r := range list {
			if r.OK && (files == nil || r.Time > files.Time) {
				files = &VerifiedRestore{Subject: plan, Time: r.Time}
			}
		}
	}
	return db, files
}

// SetTags replaces the Relyper Backup tags of an app. Each tag forms a
// group that can get its own backup plan.
func (s *Service) SetTags(ctx context.Context, appID string, tags []string, actor string) ([]string, error) {
	if !discovery.ValidAppID(appID) || strings.HasPrefix(appID, discovery.KindGroup+":") {
		return nil, fmt.Errorf("invalid app ID %q", appID)
	}
	if s.s.TagsPath == "" {
		return nil, errors.New("no place to store tags")
	}
	clean, err := discovery.CleanTags(tags)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	all, err := discovery.LoadTags(s.s.TagsPath)
	if err == nil {
		all[appID] = clean
		err = discovery.SaveTags(s.s.TagsPath, all)
	}
	s.inv = nil // rebuild the groups
	s.mu.Unlock()
	if err != nil {
		s.record(actor, "tags.set", "error", appID+": "+err.Error())
		return nil, err
	}
	s.record(actor, "tags.set", "ok", fmt.Sprintf("%s: %s", appID, strings.Join(clean, ", ")))
	return clean, nil
}
