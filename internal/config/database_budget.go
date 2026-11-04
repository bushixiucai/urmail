package config

import (
	"fmt"
	"strconv"
	"time"

	"github.com/bushixiucai/urmail/internal/db"
)

// DatabaseBudgets cap connections per role pool (not across PostgreSQL roles).
// Defaults total 27 daemon connections: core 8+8, AI 2+2, marketing 2+2,
// background 3. PostgreSQL must reserve additional mail/agent/operator capacity.
type DatabaseBudgets struct{ Core, AI, Marketing, Background db.Budget }

func loadDatabaseBudgets(values map[string]string) (DatabaseBudgets, error) {
	result := DatabaseBudgets{}
	for _, item := range []struct {
		name   string
		target *db.Budget
		conns  int32
	}{
		{"CORE", &result.Core, 8}, {"AI", &result.AI, 2}, {"MARKETING", &result.Marketing, 2}, {"BACKGROUND", &result.Background, 3},
	} {
		b := db.Budget{MaxConns: item.conns, MaxQueue: 4, AcquireTimeout: 250 * time.Millisecond, OperationTimeout: 30 * time.Second}
		if item.name == "CORE" {
			b.MaxQueue = 32
		}
		prefix := "URMAIL_DB_" + item.name + "_"
		for _, field := range []struct {
			key string
			set func(int64)
		}{
			{"MAX_CONNS", func(v int64) {
				if v >= 0 && v <= 1024 {
					b.MaxConns = int32(v)
				}
			}}, {"MAX_QUEUE", func(v int64) { b.MaxQueue = int(v) }},
		} {
			if raw := values[prefix+field.key]; raw != "" {
				v, err := strconv.ParseInt(raw, 10, 32)
				if err != nil || v < 0 || v > 1024 {
					return result, fmt.Errorf("%s%s must be between 0 and 1024", prefix, field.key)
				}
				field.set(v)
			}
		}
		for _, field := range []struct {
			key    string
			target *time.Duration
		}{
			{"ACQUIRE_TIMEOUT", &b.AcquireTimeout}, {"OPERATION_TIMEOUT", &b.OperationTimeout},
		} {
			if raw := values[prefix+field.key]; raw != "" {
				v, err := time.ParseDuration(raw)
				if err != nil || v < time.Millisecond || v > 5*time.Minute {
					return result, fmt.Errorf("%s%s must be between 1ms and 5m", prefix, field.key)
				}
				*field.target = v
			}
		}
		if b.MaxConns < 1 {
			return result, fmt.Errorf("%sMAX_CONNS must be positive", prefix)
		}
		if item.name != "CORE" && b.MaxConns > 4 {
			return result, fmt.Errorf("%sMAX_CONNS must not exceed 4", prefix)
		}
		*item.target = b
	}
	return result, nil
}
