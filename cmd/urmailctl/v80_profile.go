package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

const v80Profile = "v80-preserve"
const v80MaxMembers = 250000
const v80MaxBytes int64 = 1 << 40

type v80RootSpec struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Required  bool   `json:"required"`
}

var v80Roots = []v80RootSpec{
	{"etc/mailinabox.conf", false, true},
	{"etc/default/postgrey", false, true}, {"etc/opendkim", true, true},
	{"etc/systemd/system/mailinabox.service", false, false}, {"var/spool/cron/crontabs/root", false, false},
	{"etc/cron.d/mailinabox", false, false}, {"etc/cron.daily/mailinabox", false, false},
	{"etc/cron.hourly/mailinabox", false, false},
	{"etc/cron.daily/mailinabox-postgrey-whitelist", false, false},
	{"etc/cron.daily/mailinabox-ssl-cleanup", false, false},
	{"etc/cron.daily/mailinabox-dnssec", false, false}, {"etc/cron.weekly/mailinabox", false, false},
	{"home/user-data/mail", true, true}, {"home/user-data/ssl", true, true}, {"home/user-data/owncloud", true, true},
	{"usr/local/lib/roundcubemail", true, true}, {"usr/local/lib/owncloud", true, true}, {"usr/local/lib/z-push", true, true}, {"var/lib/z-push", true, true},
	{"etc/postfix", true, true}, {"etc/dovecot", true, true}, {"etc/nginx", true, true}, {"etc/php", true, true},
	{"home/user-data/dns", true, false}, {"home/user-data/www", true, false}, {"etc/nsd", true, false}, {"etc/opendkim.conf", false, false}, {"etc/default/opendkim", false, false}, {"etc/letsencrypt", true, false},
	{"etc/cron.d/mailinabox-nightly", false, false}, {"etc/cron.d/mailinabox-nextcloud", false, false}, {"lib/systemd/system/mailinabox.service", false, false},
	{"usr/share/nginx/modules-available", true, false},
	{"usr/share/postfix/makedefs.out", false, false},
	{"etc/ssl/private/ssl-cert-snakeoil.key", false, false},
	{"etc/ssl/certs/ssl-cert-snakeoil.pem", false, false},
}
var v80RequiredLayout = map[string]string{
	"etc/mailinabox.conf":                               "file",
	"home/user-data/mail/users.sqlite":                  "file",
	"home/user-data/mail/mailboxes":                     "directory",
	"home/user-data/mail/sieve":                         "directory",
	"home/user-data/mail/roundcube/roundcube.sqlite":    "file",
	"home/user-data/owncloud/owncloud.db":               "file",
	"home/user-data/ssl/ssl_private_key.pem":            "persistent_file",
	"home/user-data/ssl/ssl_certificate.pem":            "persistent_file",
	"home/user-data/mail/dkim/mail.private":             "persistent_file",
	"home/user-data/owncloud/config.php":                "file",
	"usr/local/lib/roundcubemail/config/config.inc.php": "file",
	"usr/local/lib/owncloud/config/config.php":          "symlink",
}
var v80FollowUp = []string{"isolated_native_complete_restore", "managed_writer_and_cron_fencing", "same_postgresql_instance_nextcloud_schema_owner", "source_dsn_and_secret_rebind", "roundcube_nextcloud_session_invalidation", "device_state_and_external_replay_reconciliation", "generation_protocol_and_real_client_acceptance", "manual_direct_sender_login_auto_receive_only_parity", "application_runtime_os_package_reconstruction", "postfix_queue_and_remote_side_effect_reconciliation"}

type v80Member struct {
	Name               string    `json:"name"`
	Kind               string    `json:"kind"`
	Size               int64     `json:"size"`
	Mode               int64     `json:"mode"`
	UID                int       `json:"uid"`
	GID                int       `json:"gid"`
	ModifiedAt         time.Time `json:"modified_at"`
	LinkTarget         string    `json:"link_target,omitempty"`
	SHA256             string    `json:"sha256,omitempty"`
	info               fs.FileInfo
	rootPath, relative string
	rootInfo           fs.FileInfo
}
type v80Exclusion struct{ Name, Target, Reason string }
type v80Inventory struct {
	Members      []v80Member
	Excluded     []v80Exclusion
	Blockers     []string
	Bytes        int64
	LayoutSHA256 string
}
type v80Manifest struct {
	Version                 int            `json:"version"`
	Profile                 string         `json:"profile"`
	ProfileVersion          int            `json:"profile_version"`
	SpecSHA256              string         `json:"spec_sha256"`
	Scope                   string         `json:"scope"`
	CreatedAt               time.Time      `json:"created_at"`
	OfflineConfirmed        bool           `json:"offline_confirmed"`
	LiveWriterCheck         bool           `json:"live_writer_check"`
	LayoutSHA256            string         `json:"layout_sha256"`
	Artifact                backupArtifact `json:"artifact"`
	Members                 []v80Member    `json:"members"`
	Excluded                []v80Exclusion `json:"excluded_runtime_links"`
	RestoreApplyAvailable   bool           `json:"restore_apply_available"`
	SQLiteIntegrityVerified bool           `json:"sqlite_integrity_verified"`
	RequiredFollowUp        []string       `json:"required_follow_up"`
}

func v80SpecDigest() string {
	raw, _ := json.Marshal(struct {
		Roots      []v80RootSpec
		Required   map[string]string
		LinkPolicy string
	}{v80Roots, v80RequiredLayout, "preserved-within-profile-or-explicit-roundcube-runtime-exclusion-v1"})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func v80Allowed(name string) bool {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\x00\r\n") {
		return false
	}
	for _, root := range v80Roots {
		if name == root.Path || (root.Directory && strings.HasPrefix(name, root.Path+"/")) {
			return true
		}
	}
	return false
}
func v80LinkTarget(name, target string) string {
	if path.IsAbs(target) {
		return path.Clean(strings.TrimPrefix(target, "/"))
	}
	return path.Clean(path.Join(path.Dir(name), target))
}
func v80RuntimeLink(name, target string) bool {
	target = path.Clean("/" + v80LinkTarget(name, target))
	return (name == "usr/local/lib/roundcubemail/temp" && target == "/var/tmp/roundcubemail") || (name == "usr/local/lib/roundcubemail/logs" && target == "/var/log/roundcubemail")
}
func v80MemberMarker(member v80Member) string {
	sum := sha256.Sum256([]byte(member.Kind + "\x00" + member.LinkTarget))
	return hex.EncodeToString(sum[:])
}
func v80SameInfo(a, b fs.FileInfo) bool {
	if !os.SameFile(a, b) || a.Size() != b.Size() || a.Mode() != b.Mode() || !a.ModTime().Equal(b.ModTime()) {
		return false
	}
	ah, ae := tar.FileInfoHeader(a, "")
	bh, be := tar.FileInfoHeader(b, "")
	return ae == nil && be == nil && ah.Uid == bh.Uid && ah.Gid == bh.Gid
}

func validateV80Layout(members []v80Member) error {
	seen := map[string]v80Member{}
	var total int64
	if len(members) == 0 || len(members) > v80MaxMembers {
		return errors.New("profile member limit is invalid")
	}
	for _, member := range members {
		if !v80Allowed(member.Name) || member.Size < 0 || member.Size > v80MaxBytes || member.UID < 0 || member.GID < 0 || member.Mode < 0 || member.Mode > 0o7777 {
			return errors.New("profile member metadata is invalid")
		}
		if _, duplicate := seen[member.Name]; duplicate {
			return errors.New("profile contains duplicate members")
		}
		switch member.Kind {
		case "file":
			if member.LinkTarget != "" {
				return errors.New("regular profile member has a link target")
			}
			if member.Mode&0o6000 != 0 {
				return errors.New("profile contains privileged executable mode")
			}
			total += member.Size
		case "directory":
			if member.Size != 0 || member.LinkTarget != "" {
				return errors.New("invalid directory metadata")
			}
		case "symlink":
			if member.Size != 0 || member.LinkTarget == "" || !v80Allowed(v80LinkTarget(member.Name, member.LinkTarget)) {
				return errors.New("profile symlink escapes its exact roots")
			}
		default:
			return errors.New("profile contains unsupported object type")
		}
		if total > v80MaxBytes {
			return errors.New("profile exceeds its byte limit")
		}
		seen[member.Name] = member
	}
	for _, root := range v80Roots {
		if root.Required {
			member, ok := seen[root.Path]
			kind := "file"
			if root.Directory {
				kind = "directory"
			}
			if !ok || member.Kind != kind {
				return errors.New("required profile root is missing")
			}
		}
	}
	for name, kind := range v80RequiredLayout {
		member, ok := seen[name]
		valid := ok && member.Kind == kind
		if kind == "persistent_file" {
			valid = ok && (member.Kind == "file" || member.Kind == "symlink")
		}
		if !valid {
			return errors.New("required legacy persistent layout is incomplete")
		}
	}
	ncConfig := seen["usr/local/lib/owncloud/config/config.php"]
	if v80LinkTarget(ncConfig.Name, ncConfig.LinkTarget) != "home/user-data/owncloud/config.php" {
		return errors.New("legacy groupware config link does not reference its preserved private configuration")
	}
	for _, member := range members {
		if member.Kind == "symlink" {
			target := v80LinkTarget(member.Name, member.LinkTarget)
			visited := map[string]bool{member.Name: true}
			for depth := 0; ; depth++ {
				if depth > 64 || visited[target] {
					return errors.New("profile contains cyclic symlinks")
				}
				visited[target] = true
				destination, ok := seen[target]
				if !ok {
					return errors.New("profile symlink target is not preserved")
				}
				if destination.Kind != "symlink" {
					if v80RequiredLayout[member.Name] == "persistent_file" && destination.Kind != "file" {
						return errors.New("required persistent key or certificate is not a file")
					}
					break
				}
				target = v80LinkTarget(destination.Name, destination.LinkTarget)
			}
		}
	}
	return nil
}

var v80StorageRootPattern = regexp.MustCompile(`(?m)^STORAGE_ROOT=(?:"/home/user-data"|'/home/user-data'|/home/user-data)\s*$`)
var v80FPMPattern = regexp.MustCompile(`(?m)^\s*listen\s*=\s*/run/php/mailinabox-fpm\.sock\s*(?:;[^\n]*)?$`)

func validateV80ConfigMaterial(configs map[string][]byte, fpmFound bool) error {
	if len(v80StorageRootPattern.FindAll(configs["etc/mailinabox.conf"], -1)) != 1 {
		return errors.New("profile requires the exact legacy storage root")
	}
	php := string(configs["home/user-data/owncloud/config.php"])
	for key, value := range map[string]string{"datadirectory": "/home/user-data/owncloud", "dbtype": "sqlite3", "dbname": "owncloud"} {
		pattern := regexp.MustCompile(`['"]` + key + `['"]\s*=>\s*['"]` + regexp.QuoteMeta(value) + `['"]`)
		occurrences := regexp.MustCompile(`['"]`+key+`['"]\s*=>`).FindAllString(php, -1)
		if len(occurrences) != 1 || len(pattern.FindAllString(php, -1)) != 1 {
			return errors.New("legacy groupware datastore is not the preserved fixed SQLite layout")
		}
	}
	rc := string(configs["usr/local/lib/roundcubemail/config/config.inc.php"])
	if !regexp.MustCompile(`['"]db_dsnw['"]\]\s*=\s*['"]sqlite:/+home/user-data/mail/roundcube/roundcube\.sqlite(?:\?mode=0640)?['"]`).MatchString(rc) {
		return errors.New("legacy webmail datastore requires explicit profile review")
	}
	if !fpmFound {
		return errors.New("legacy FPM listener is not represented by the profile")
	}
	for _, name := range []string{"home/user-data/mail/users.sqlite", "home/user-data/mail/roundcube/roundcube.sqlite", "home/user-data/owncloud/owncloud.db"} {
		if !strings.HasPrefix(string(configs[name]), "SQLite format 3\x00") {
			return errors.New("legacy SQLite artifact header is invalid")
		}
	}
	return nil
}
func v80ConfigurationMember(name string) bool {
	return name == "etc/mailinabox.conf" || name == "home/user-data/owncloud/config.php" || name == "usr/local/lib/roundcubemail/config/config.inc.php" || name == "home/user-data/mail/users.sqlite" || name == "home/user-data/mail/roundcube/roundcube.sqlite" || name == "home/user-data/owncloud/owncloud.db" || (strings.HasPrefix(name, "etc/php/") && strings.HasSuffix(name, ".conf"))
}
func v80LayoutDigest(members []v80Member, excluded []v80Exclusion) string {
	copyMembers := append([]v80Member(nil), members...)
	for i := range copyMembers {
		copyMembers[i].SHA256 = ""
	}
	raw, _ := json.Marshal(struct {
		Members  []v80Member
		Excluded []v80Exclusion
	}{copyMembers, excluded})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func v80SortInventory(inventory *v80Inventory) {
	sort.Slice(inventory.Members, func(i, j int) bool { return inventory.Members[i].Name < inventory.Members[j].Name })
	inventory.LayoutSHA256 = v80LayoutDigest(inventory.Members, inventory.Excluded)
}
