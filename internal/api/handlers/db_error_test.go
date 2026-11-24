package handlers

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/bushixiucai/urmail/internal/db/dberr"
)

func testGinContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/archive/cases", nil)
	return c, recorder
}

func TestWriteDatabaseErrorMapsFrozenStatuses(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		status       int
		conflictCode string
		bodyCode     string
	}{
		{"unique constraint", &pgconn.PgError{Code: "23505"}, 409, "TEST_STALE", "TEST_STALE"},
		{"check constraint", &pgconn.PgError{Code: "23514"}, 409, "TEST_STALE", "TEST_STALE"},
		{"foreign key", &pgconn.PgError{Code: "23503"}, 404, "TEST_STALE", "NOT_FOUND"},
		{"permission", &pgconn.PgError{Code: "42501"}, 403, "TEST_STALE", "FORBIDDEN"},
		{"unavailable", &pgconn.PgError{Code: "57014"}, 503, "TEST_STALE", "ARCHIVE_UNAVAILABLE"},
		{"sentinel", dberr.ErrConstraint, 409, "TEST_STALE", "TEST_STALE"},
		{"unclassified", errors.New("driver exploded: 42"), 500, "TEST_STALE", "DB_ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, recorder := testGinContext(t)
			writeDatabaseError(c, tc.err, tc.conflictCode)
			if recorder.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, tc.status, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), `"code":"`+tc.bodyCode+`"`) {
				t.Fatalf("body %s missing code %s", recorder.Body.String(), tc.bodyCode)
			}
			if tc.status != 500 && strings.Contains(recorder.Body.String(), "driver exploded") {
				t.Fatalf("classified response leaked the driver error: %s", recorder.Body.String())
			}
		})
	}
}

// TestWriteDatabaseErrorLogsRawError proves the frozen rule that an
// unclassifiable failure is 500 AND the raw driver error reaches the log.
func TestWriteDatabaseErrorLogsRawError(t *testing.T) {
	var buffer bytes.Buffer
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(&buffer), zapcore.DebugLevel)
	logger := zap.New(core)
	previous := zap.L()
	zap.ReplaceGlobals(logger)
	defer zap.ReplaceGlobals(previous)

	c, recorder := testGinContext(t)
	writeDatabaseError(c, errors.New("driver exploded: disk I/O error"), "TEST_STALE")
	if recorder.Code != 500 {
		t.Fatalf("status=%d want 500", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "DB_ERROR") || strings.Contains(body, "driver exploded") {
		t.Fatalf("response body must be the fixed DB_ERROR: %s", body)
	}
	logged := buffer.String()
	if !strings.Contains(logged, "driver exploded: disk I/O error") {
		t.Fatalf("raw driver error not logged: %s", logged)
	}
	if !strings.Contains(logged, "database statement failed") {
		t.Fatalf("missing action in log: %s", logged)
	}
}

func TestCatchAllDelegatesToTheSingleMapping(t *testing.T) {
	c, recorder := testGinContext(t)
	recallProductError(c, pgx.ErrNoRows)
	if recorder.Code != 409 || !strings.Contains(recorder.Body.String(), "RECALL_REVIEW_STALE") {
		t.Fatalf("recall no-rows: %d %s", recorder.Code, recorder.Body.String())
	}
	c, recorder = testGinContext(t)
	archiveCaseError(c, pgx.ErrNoRows)
	if recorder.Code != 404 || !strings.Contains(recorder.Body.String(), "NOT_FOUND") {
		t.Fatalf("archive no-rows: %d %s", recorder.Code, recorder.Body.String())
	}
	c, recorder = testGinContext(t)
	archiveCaseError(c, errors.New("opaque failure"))
	if recorder.Code != 500 {
		t.Fatalf("archive unclassified: %d %s", recorder.Code, recorder.Body.String())
	}
	c, recorder = testGinContext(t)
	recallProductError(c, &pgconn.PgError{Code: "42501"})
	if recorder.Code != 403 {
		t.Fatalf("recall permission: %d %s", recorder.Code, recorder.Body.String())
	}
}
