package config

import (
	"testing"
	"time"
)

func TestDatabaseBudgetDefaultsAndOverrides(t *testing.T) {
	b, err := loadDatabaseBudgets(nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Core.MaxConns != 8 || b.Marketing.MaxConns != 2 || b.Background.MaxConns != 3 || b.AI.MaxQueue != 4 {
		t.Fatalf("defaults %+v", b)
	}
	b, err = loadDatabaseBudgets(map[string]string{"URMAIL_DB_AI_MAX_CONNS": "1", "URMAIL_DB_AI_MAX_QUEUE": "0", "URMAIL_DB_AI_ACQUIRE_TIMEOUT": "15ms"})
	if err != nil || b.AI.MaxConns != 1 || b.AI.MaxQueue != 0 || b.AI.AcquireTimeout != 15*time.Millisecond {
		t.Fatalf("override %+v %v", b, err)
	}
}
func TestDatabaseBudgetRejectsUnsafeValues(t *testing.T) {
	for _, key := range []string{"MAX_CONNS", "MAX_QUEUE", "ACQUIRE_TIMEOUT", "OPERATION_TIMEOUT"} {
		for _, value := range []string{"-1", "garbage", "9999999999999999999999"} {
			if _, err := loadDatabaseBudgets(map[string]string{"URMAIL_DB_AI_" + key: value}); err == nil {
				t.Fatalf("accepted %s=%s", key, value)
			}
		}
	}
	for _, value := range []string{"0", "5"} {
		if _, err := loadDatabaseBudgets(map[string]string{"URMAIL_DB_MARKETING_MAX_CONNS": value}); err == nil {
			t.Fatal(value)
		}
	}
}
