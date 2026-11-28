// Relyper-owned: tags that group apps inside Relyper Backup only.
//
// Easypanel projects are coarse buckets (the free tier allows three), so
// the apps of one real project are often spread over several Easypanel
// projects and one Easypanel project holds unrelated apps. A tag groups
// apps the way the user thinks of them. Every tag forms a group app
// (group:<tag>) that can get its own backup plan, with the services and
// data of all its members. Tags are stored next to the configuration
// (relyper-tags.json in the config directory), so a backup of /config
// keeps them.
package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// TagsFileName is the file in the config directory that holds the tags.
const TagsFileName = "relyper-tags.json"

// TagsPath is relyper-tags.json next to the Backrest config file. The
// caller passes env.ConfigFilePath(): this package is also built into the
// Docker guard image, which holds only internal/relyper.
func TagsPath(configFile string) string {
	return filepath.Join(filepath.Dir(configFile), TagsFileName)
}

var tagRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,39}$`)

// NormalizeTag lowercases a tag and turns spaces into dashes. It returns
// "" for a tag that is not allowed (letters, digits, '-', '_', '.', at most
// 40 characters).
func NormalizeTag(t string) string {
	t = strings.ToLower(strings.Join(strings.Fields(t), "-"))
	if !tagRe.MatchString(t) {
		return ""
	}
	return t
}

// GroupID is the app ID of the group formed by a tag.
func GroupID(tag string) string { return KindGroup + ":" + tag }

// Tags maps app IDs to their tags.
type Tags map[string][]string

type tagsFile struct {
	Version int  `json:"version"`
	Apps    Tags `json:"apps"`
}

// LoadTags reads the tags; a missing file means no tags.
func LoadTags(path string) (Tags, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Tags{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f tagsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Apps == nil {
		f.Apps = Tags{}
	}
	return f.Apps, nil
}

// SaveTags writes the tags atomically.
func SaveTags(path string, t Tags) error {
	clean := Tags{}
	for id, list := range t {
		if len(list) > 0 {
			clean[id] = list
		}
	}
	b, err := json.MarshalIndent(tagsFile{Version: 1, Apps: clean}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// CleanTags normalizes, deduplicates and sorts a tag list. It fails on a
// tag that is not allowed.
func CleanTags(in []string) ([]string, error) {
	set := map[string]bool{}
	for _, t := range in {
		if strings.TrimSpace(t) == "" {
			continue
		}
		n := NormalizeTag(t)
		if n == "" {
			return nil, fmt.Errorf("tag %q: use letters, digits, '-', '_' or '.', at most 40 characters", t)
		}
		set[n] = true
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

// ApplyTags marks the apps with their tags and adds one group app per tag
// with the services of all its members (service names get the member's
// name in front: "shop.db"). Tags of apps that are not on this host are
// ignored. Group apps are added at most once.
func (inv *Inventory) ApplyTags(t Tags) {
	if inv == nil || len(t) == 0 {
		return
	}
	members := map[string][]*App{}
	for i := range inv.Apps {
		a := &inv.Apps[i]
		if a.Kind == KindGroup {
			return // already applied
		}
		a.Tags = append([]string(nil), t[a.ID]...)
		for _, tag := range a.Tags {
			members[tag] = append(members[tag], a)
		}
	}
	tags := make([]string, 0, len(members))
	for tag := range members {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		g := merge(GroupID(tag), tag, KindGroup, members[tag], true)
		g.Tags = []string{tag}
		inv.Apps = append(inv.Apps, *g)
	}
}
