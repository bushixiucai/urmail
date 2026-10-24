package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestNotificationLocaleUsesExistingPreferencesAndOneCatalog(t *testing.T) {
	p := notificationDatabase(t)
	ctx := t.Context()
	q := store.New(p)
	box, err := secrets.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(SMTPConfig{Enabled: true, Host: "relay.example.test", Port: 465, From: "notice@example.test", ImplicitTLS: true})
	cipher, err := box.EncryptString(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, _ := json.Marshal(cipher)
	if err = q.PlatformUpsertSetting(ctx, store.PlatformUpsertSettingParams{Key: "notification_channel", Value: encrypted}); err != nil {
		t.Fatal(err)
	}
	var brand, tenant, user int64
	if err = p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('notice-locale','Locale') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug) VALUES($1,'Locale','notice-locale') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO users(email,password_hash,locale) VALUES('english@example.test','unused','en') RETURNING id").Scan(&user); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := p.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	worker := Worker{Pool: p, Secrets: box, Messages: messages, BaseURL: "https://panel.example.test", FallbackLocale: "en", Enabled: true}
	cases := []struct {
		name                                                                  string
		userID                                                                any
		tenantID                                                              any
		userLocale, tenantLocale, brandLocale, platformLocale, fallback, want string
	}{
		{"user overrides tenant", user, tenant, "en", "zh-CN", "zh-CN", "zh-CN", "zh-CN", "en"},
		{"tenant overrides brand", nil, tenant, "en", "en", "zh-CN", "zh-CN", "zh-CN", "en"},
		{"brand overrides platform", nil, tenant, "en", "", "en", "zh-CN", "zh-CN", "en"},
		{"platform inherited", nil, tenant, "en", "", "", "en", "zh-CN", "en"},
		{"contact without preference uses configured default", nil, nil, "en", "", "", "", "en", "en"},
		{"missing system locale uses catalog default", nil, nil, "en", "", "", "", "", i18n.DefaultLocale},
		{"unknown user locale uses tenant", user, tenant, "unknown", "en", "zh-CN", "zh-CN", "zh-CN", "en"},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec("UPDATE users SET locale=$1 WHERE id=$2", tc.userLocale, user)
			exec("DELETE FROM settings WHERE key='locale'")
			for _, setting := range []struct {
				scope  string
				id     int64
				locale string
			}{{"platform", 0, tc.platformLocale}, {"brand", brand, tc.brandLocale}, {"tenant", tenant, tc.tenantLocale}} {
				if setting.locale != "" {
					value, _ := json.Marshal(setting.locale)
					exec("INSERT INTO settings(scope,scope_id,key,value) VALUES($1,$2,'locale',$3)", setting.scope, setting.id, value)
				}
			}
			params := map[string]any{"days": 3, "plan": "Paid plan", "period_end": "2026-12-01"}
			encoded, _ := json.Marshal(params)
			exec("INSERT INTO notification_outbox(tenant_id,user_id,kind,message_key,params,dedupe_key,channel,state,recipient,recipient_verified) VALUES($1,$2,'subscription_expiry','notification.subscription_expiry',$3,$4,'email','pending','english@example.test',true)", tc.tenantID, tc.userID, encoded, fmt.Sprintf("locale-%d", index))
			sent := 0
			worker.FallbackLocale = tc.fallback
			worker.Send = func(_ context.Context, _ SMTPConfig, m Message) error {
				sent++
				catalog := messages.Get(tc.want)
				if m.Subject != catalog.Lookup("notification.subject", nil) || m.Body != catalog.Lookup("notification.subscription_expiry", params) {
					t.Fatalf("body/subject not from %s catalog: subject=%q body=%q", tc.want, m.Subject, m.Body)
				}
				if m.To != "english@example.test" {
					t.Fatalf("wrong recipient %q", m.To)
				}
				return nil
			}
			if worked, e := worker.Step(ctx); !worked || e != nil || sent != 1 {
				t.Fatalf("localized delivery worked=%v sent=%d err=%v", worked, sent, e)
			}
		})
	}
}
