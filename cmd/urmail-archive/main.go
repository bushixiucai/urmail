// Command urmail-archive is the Postfix pipe transport that stores a copy of
// each delivered message and records it in archive_entries.
//
// It is deliberately soft-fail: any error is logged and the process exits 0,
// so an archive outage never blocks or bounces the real delivery.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

const (
	archiveRoot = "/var/lib/urmail/archive"
	maxMessage  = 64 << 20
)

func main() {
	fs := flag.NewFlagSet("archive-pipe", flag.ContinueOnError)
	recipient := fs.String("recipient", "", "Postfix envelope recipient")
	envFile := fs.String("env-file", "/etc/urmail/urmaild.env", "urmaild environment file")
	if err := fs.Parse(os.Args[1:]); err != nil {
		log.Printf("archive-pipe: %v", err)
		return
	}
	if *recipient == "" {
		log.Printf("archive-pipe: empty recipient")
		return
	}
	if err := run(*recipient, *envFile); err != nil {
		log.Printf("archive-pipe: %v", err)
	}
}

func run(recipient, envFile string) error {
	original, err := decodeRecipient(recipient)
	if err != nil {
		return err
	}
	_ = os.Setenv("URMAIL_ENV_FILE", envFile)
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	q := store.New(pool.Pool)
	mailbox, err := q.PlatformGetMailboxByAddress(ctx, strings.ToLower(original))
	if err != nil {
		return fmt.Errorf("resolve mailbox %s: %w", original, err)
	}

	raw, err := io.ReadAll(io.LimitReader(os.Stdin, maxMessage+1))
	if err != nil {
		return fmt.Errorf("read message: %w", err)
	}
	if len(raw) > maxMessage {
		return fmt.Errorf("message exceeds archive limit")
	}

	messageID, subject, from, to := parseHeaders(raw)
	if messageID == "" {
		messageID = fallbackMessageID(raw)
	}
	dir := filepath.Join(archiveRoot, fmt.Sprintf("%d", mailbox.TenantID), fmt.Sprintf("%d", mailbox.ID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create archive directory: %w", err)
	}
	path := filepath.Join(dir, sanitize(messageID)+"-"+randomSuffix()+".eml")
	if err := writePrivate(path, raw); err != nil {
		return fmt.Errorf("write archive message: %w", err)
	}

	err = q.PlatformInsertArchiveEntry(ctx, store.PlatformInsertArchiveEntryParams{
		TenantID:   mailbox.TenantID,
		MailboxID:  pgtype.Int8{Int64: mailbox.ID, Valid: true},
		MessageID:  messageID,
		Path:       path,
		Subject:    pgtype.Text{String: subject, Valid: subject != ""},
		FromAddr:   pgtype.Text{String: from, Valid: from != ""},
		ToAddr:     pgtype.Text{String: to, Valid: to != ""},
		ReceivedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		SizeBytes:  int64(len(raw)),
	})
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("insert archive entry: %w", err)
	}
	return nil
}

func decodeRecipient(recipient string) (string, error) {
	const prefix = "archive+"
	if !strings.HasPrefix(recipient, prefix) {
		return "", fmt.Errorf("unexpected archive recipient %q", recipient)
	}
	encoded := strings.TrimPrefix(recipient, prefix)
	if at := strings.IndexByte(encoded, '@'); at >= 0 {
		encoded = encoded[:at]
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode archive recipient: %w", err)
	}
	return string(raw), nil
}

func parseHeaders(raw []byte) (messageID, subject, from, to string) {
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return "", "", "", ""
	}
	messageID = strings.TrimSpace(msg.Header.Get("Message-Id"))
	subject = decodeHeader(msg.Header.Get("Subject"))
	from = strings.TrimSpace(msg.Header.Get("From"))
	to = strings.TrimSpace(msg.Header.Get("To"))
	return
}

func decodeHeader(value string) string {
	if value == "" {
		return ""
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}

func fallbackMessageID(raw []byte) string {
	encoded, _ := json.Marshal(len(raw))
	sum := hex.EncodeToString([]byte(fmt.Sprintf("%x", encoded)))
	return "archive-" + sum
}

func sanitize(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
		if b.Len() >= 120 {
			break
		}
	}
	if b.Len() == 0 {
		return "message"
	}
	return b.String()
}

func randomSuffix() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func writePrivate(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}
