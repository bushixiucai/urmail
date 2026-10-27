package eas

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// Session is the authenticated mailbox principal carried from the API
// middleware. A request without one never reaches the command layer.
type Session = webmail.Session

// Device is the persisted per-device transport state.
type Device struct {
	DeviceID        string
	ProtocolVersion string
	UserAgent       string
	PolicyKey       int64
	Provisioned     bool
}

// Folder is one entry of the exposed hierarchy: an email special folder plus
// the default addressbook and default calendar from the collab tables.
type Folder struct {
	ID          string // opaque collection id, e.g. "e:INBOX", "ab:17", "cal:3"
	ParentID    string
	DisplayName string
	Type        int
	Class       string // Email, Calendar, Contacts
}

// ErrNotFound marks a missing device or snapshot row.
var ErrNotFound = errors.New("eas: not found")

// UpsertDevice stores or refreshes device state for this mailbox only. The
// MailboxTx scope makes cross-mailbox writes impossible (RLS + predicates).
func UpsertDevice(ctx context.Context, pool *db.Pool, sess Session, d Device) error {
	tx, err := pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx,
		`INSERT INTO mailbox_eas_devices
			   (tenant_id, mailbox_id, device_id, protocol_version, user_agent,
			    last_seen_at)
			 VALUES (current_setting('urmail.tenant_id')::bigint,
			         current_setting('urmail.mailbox_id')::bigint, $1, $2, $3, now())
			 ON CONFLICT (tenant_id, mailbox_id, device_id) DO UPDATE SET
			   protocol_version = EXCLUDED.protocol_version,
			   user_agent = EXCLUDED.user_agent,
			   last_seen_at = now()`,
		d.DeviceID, d.ProtocolVersion, d.UserAgent)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetDevice loads device state, scoped to the session mailbox.
func GetDevice(ctx context.Context, pool *db.Pool, sess Session, deviceID string) (Device, error) {
	tx, err := pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return Device{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row := tx.QueryRow(ctx,
		`SELECT device_id, protocol_version, user_agent, policy_key, provisioned
			   FROM mailbox_eas_devices
			  WHERE tenant_id = $1 AND mailbox_id = $2 AND device_id = $3`,
		sess.TenantID, sess.MailboxID, deviceID)
	var d Device
	err = row.Scan(&d.DeviceID, &d.ProtocolVersion, &d.UserAgent, &d.PolicyKey, &d.Provisioned)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return Device{}, err
	}
	return d, nil
}

// SetPolicyKey persists the acknowledged provisioning policy key.
func SetPolicyKey(ctx context.Context, pool *db.Pool, sess Session, deviceID string, key int64, provisioned bool) error {
	tx, err := pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx,
		`UPDATE mailbox_eas_devices
			    SET policy_key = $4, provisioned = $5, last_seen_at = now()
			  WHERE tenant_id = $1 AND mailbox_id = $2 AND device_id = $3`,
		sess.TenantID, sess.MailboxID, deviceID, key, provisioned)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// LoadFolderSnapshot returns the folder diff state stored for this device.
func LoadFolderSnapshot(ctx context.Context, pool *db.Pool, sess Session, deviceID string) ([]Folder, error) {
	tx, err := pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx,
		`SELECT folder_id, parent_id, display_name, folder_type, folder_class
			   FROM mailbox_eas_folder_state
			  WHERE tenant_id = $1 AND mailbox_id = $2 AND device_id = $3
			  ORDER BY id`,
		sess.TenantID, sess.MailboxID, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Folder
	for rows.Next() {
		var f Folder
		if err = rows.Scan(&f.ID, &f.ParentID, &f.DisplayName, &f.Type, &f.Class); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ReplaceFolderSnapshot atomically stores the current hierarchy for a device,
// tagged with the change-log sequence the snapshot reflects.
func ReplaceFolderSnapshot(ctx context.Context, pool *db.Pool, sess Session, deviceID string, folders []Folder, changeSeq int64) error {
	tx, err := pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx,
		`DELETE FROM mailbox_eas_folder_state
			  WHERE tenant_id = $1 AND mailbox_id = $2 AND device_id = $3`,
		sess.TenantID, sess.MailboxID, deviceID); err != nil {
		return err
	}
	for _, f := range folders {
		if _, err = tx.Exec(ctx,
			`INSERT INTO mailbox_eas_folder_state
				   (tenant_id, mailbox_id, device_id, folder_id, parent_id,
				    display_name, folder_type, folder_class, change_seq)
				 VALUES (current_setting('urmail.tenant_id')::bigint,
				         current_setting('urmail.mailbox_id')::bigint,
				         $1, $2, $3, $4, $5, $6, $7)`,
			deviceID, f.ID, f.ParentID, f.DisplayName, f.Type, f.Class, changeSeq); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// MaxChangeID returns the newest change-log id for this mailbox (0 when none).
func MaxChangeID(ctx context.Context, pool *db.Pool, sess Session) (int64, error) {
	tx, err := pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id int64
	err = tx.QueryRow(ctx,
		`SELECT coalesce(max(id), 0) FROM mailbox_changes
			  WHERE tenant_id = $1 AND mailbox_id = $2`,
		sess.TenantID, sess.MailboxID).Scan(&id)
	return id, err
}

// ChangedCollections lists collection ids with change-log rows after afterID.
// Email folder activity is intentionally not covered by the change log; Ping
// also accepts the device id so later tasks can hook mail activity in.
func ChangedCollections(ctx context.Context, pool *db.Pool, sess Session, afterID int64) (map[string]bool, error) {
	tx, err := pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx,
		`SELECT resource_type, resource_id FROM mailbox_changes
			  WHERE tenant_id = $1 AND mailbox_id = $2 AND id > $3`,
		sess.TenantID, sess.MailboxID, afterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var rt string
		var rid int64
		if err = rows.Scan(&rt, &rid); err != nil {
			return nil, err
		}
		switch rt {
		case "addressbook":
			out["ab:"+itoa(rid)] = true
		case "calendar":
			out["cal:"+itoa(rid)] = true
		}
	}
	return out, rows.Err()
}

// itoa is a tiny helper avoiding fmt in the hot path.
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// Now is exposed for deterministic Ping budget tests.
var Now = time.Now
