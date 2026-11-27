// relyper: Relyper Backup addition, not part of upstream Backrest.

package mcp

import (
	"strings"
	"testing"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
)

func testConfig() *v1.Config {
	return &v1.Config{
		Instance: "test",
		Repos: []*v1.Repo{{
			Id:       "r1",
			Uri:      "rest:https://user:restpass99@host/repo",
			Password: "repo-secret-123",
			Env:      []string{"AWS_SECRET_ACCESS_KEY=aws-secret-456"},
			Flags:    []string{"--option=sftp.args=-oBatchMode=yes -i /config/.backrest-ssh/id_ed25519 -oUserKnownHostsFile=/config/.backrest-ssh/known_hosts"},
			Hooks:    []*v1.Hook{{Action: &v1.Hook_ActionDiscord{ActionDiscord: &v1.Hook_Discord{WebhookUrl: "https://discord.com/api/webhooks/1/hook-token-789"}}}},
		}},
		Plans: []*v1.Plan{{Id: "p1", Repo: "r1", Paths: []string{"/data"}}},
		Auth:  &v1.Auth{Users: []*v1.User{{Name: "jonas", Password: &v1.User_PasswordBcrypt{PasswordBcrypt: "bcrypt-hash-000"}}}},
	}
}

func TestCollectSecretsAndScrub(t *testing.T) {
	cfg := testConfig()
	sc := scrubber{secrets: collectSecrets(cfg)}
	for _, want := range []string{"repo-secret-123", "aws-secret-456", "https://discord.com/api/webhooks/1/hook-token-789", "restpass99", "bcrypt-hash-000"} {
		found := false
		for _, s := range sc.secrets {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Errorf("secret %q not collected", want)
		}
	}
	out := &struct {
		Msg   string
		List  []string
		Map   map[string]any
		Inner *OperationView
	}{
		Msg:   "error: wrong password repo-secret-123",
		List:  []string{"aws-secret-456"},
		Map:   map[string]any{"k": "x restpass99 y"},
		Inner: &OperationView{Message: "hook-token-789 failed: https://discord.com/api/webhooks/1/hook-token-789"},
	}
	sc.scrub(out)
	all := out.Msg + strings.Join(out.List, "") + out.Map["k"].(string) + out.Inner.Message
	for _, s := range []string{"repo-secret-123", "aws-secret-456", "restpass99", "discord.com/api/webhooks"} {
		if strings.Contains(all, s) {
			t.Errorf("secret %q survived scrubbing: %s", s, all)
		}
	}
}

func TestRepoViewHasNoSecrets(t *testing.T) {
	cfg := testConfig()
	v := repoView(cfg, cfg.Repos[0])
	b := redactParams(v)
	for _, s := range []string{"repo-secret-123", "aws-secret-456", "restpass99", "hook-token"} {
		if strings.Contains(b, s) {
			t.Errorf("repo view leaks %q: %s", s, b)
		}
	}
	if !v.PasswordSet || len(v.EnvNames) != 1 || v.EnvNames[0] != "AWS_SECRET_ACCESS_KEY" {
		t.Errorf("unexpected view %+v", v)
	}
	if v.SFTP == nil || v.SFTP.IdentityFile != "/config/.backrest-ssh/id_ed25519" || !v.SFTP.BatchMode {
		t.Errorf("sftp args not parsed: %+v", v.SFTP)
	}
}

func TestRedactParams(t *testing.T) {
	in := CreateRepoIn{ID: "r", URI: "rest:https://u:pw12345@h/", Password: "supersecret", Env: map[string]string{"AWS_SECRET_ACCESS_KEY": "abc12345"}}
	out := redactParams(in)
	for _, s := range []string{"supersecret", "abc12345", "pw12345"} {
		if strings.Contains(out, s) {
			t.Errorf("params leak %q: %s", s, out)
		}
	}
	if !strings.Contains(out, "AWS_SECRET_ACCESS_KEY") {
		t.Errorf("env names should stay visible: %s", out)
	}
}

func TestInputValidation(t *testing.T) {
	if err := validateEnv(map[string]string{"RESTIC_PASSWORD_COMMAND": "sh -c id"}); err == nil {
		t.Error("RESTIC_PASSWORD_COMMAND accepted")
	}
	if err := validateEnv(map[string]string{"LD_PRELOAD": "/x.so"}); err == nil {
		t.Error("LD_PRELOAD accepted")
	}
	if err := validateEnv(map[string]string{"B2_ACCOUNT_KEY": "k"}); err != nil {
		t.Error(err)
	}
	for _, f := range []string{"--password-command=id", "--files-from=/etc/shadow", "--stdin-from-command", "--one-file-system --password-command=x"} {
		if validateBackupFlags([]string{f}) == nil {
			t.Errorf("backup flag %q accepted", f)
		}
	}
	if err := validateBackupFlags([]string{"--exclude-caches", "--exclude-larger-than=2G"}); err != nil {
		t.Error(err)
	}
	for _, u := range []string{"sftp:u@-oProxyCommand=x:/p", "sftp:u@h;id:/p", "-r /tmp", "ftp://x"} {
		if validateURI(u) == nil {
			t.Errorf("uri %q accepted", u)
		}
	}
	for _, u := range []string{"sftp:u123@u123.your-storagebox.de:/backups", "/data/repo", "s3:https://s3.example/b/p", "b2:bucket:path"} {
		if err := validateURI(u); err != nil {
			t.Errorf("uri %q refused: %v", u, err)
		}
	}
	s := &Server{d: Deps{SSHDir: "/config/.backrest-ssh"}}
	if _, err := s.sftpFlag(&SFTPSettings{IdentityFile: "/k -oProxyCommand=id"}); err == nil {
		t.Error("identity file with option injection accepted")
	}
	f, err := s.sftpFlag(&SFTPSettings{IdentityFile: "/config/.backrest-ssh/id_ed25519", Port: 23})
	if err != nil {
		t.Fatal(err)
	}
	if f != "--option=sftp.args=-oBatchMode=yes -i /config/.backrest-ssh/id_ed25519 -p 23 -oUserKnownHostsFile=/config/.backrest-ssh/known_hosts" {
		t.Errorf("sftp flag %q", f)
	}
}

func TestScheduleAndRetentionMapping(t *testing.T) {
	p, err := scheduleToProto(&Schedule{Kind: "cron", Cron: "0 2 * * *"})
	if err != nil || p.GetCron() != "0 2 * * *" {
		t.Fatalf("%v %v", p, err)
	}
	if back := scheduleFromProto(p); back.Kind != "cron" || back.Clock != "local" {
		t.Errorf("round trip %+v", back)
	}
	if _, err := scheduleToProto(&Schedule{Kind: "every_days"}); err == nil {
		t.Error("every_days without days accepted")
	}
	r, err := retentionToProto(&Retention{Daily: 7, Weekly: 4})
	if err != nil || r.GetPolicyTimeBucketed().GetDaily() != 7 {
		t.Fatalf("%v %v", r, err)
	}
	if _, err := retentionToProto(&Retention{}); err == nil {
		t.Error("empty retention accepted")
	}
	if _, err := retentionToProto(&Retention{KeepAll: true, Daily: 1}); err == nil {
		t.Error("keep_all with counts accepted")
	}
}
