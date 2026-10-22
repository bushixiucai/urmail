// Package middleware holds the gin middleware used by the urMail.cn API.
package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// RequestIDHeader is the header carrying the request id.
const RequestIDHeader = "X-Request-Id"

// contextKey is the private type for context keys in this package.
type contextKey string

// RequestIDContextKey is the gin context key holding the request id.
const RequestIDContextKey contextKey = "urmail.request_id"

// RequestID assigns every request an id, reusing an inbound id when present.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if id == "" {
			id = uuid.NewString()
		}
		c.Set(string(RequestIDContextKey), id)
		c.Header(RequestIDHeader, id)
		c.Next()
	}
}

// RequestIDOf returns the request id attached to a context.
func RequestIDOf(ctx context.Context) string {
	if value, ok := ctx.Value(RequestIDContextKey).(string); ok {
		return value
	}
	return ""
}

// Recover turns a panic into a 500 response and logs the stack trace.
func Recover(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("panic recovered",
					zap.Any("panic", recovered),
					zap.String("path", c.Request.URL.Path),
					zap.String("request_id", c.GetString(string(RequestIDContextKey))),
				)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"error": gin.H{"code": "INTERNAL_ERROR"},
				})
			}
		}()
		c.Next()
	}
}

// AccessLog logs one line per request.
func AccessLog(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()

		logger.Info("http request",
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("duration", time.Since(started)),
			zap.String("request_id", c.GetString(string(RequestIDContextKey))),
			zap.String("client_ip", c.ClientIP()),
		)
	}
}

// Timeout aborts requests that outlive the given budget.
func Timeout(budget time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), budget)
		defer cancel()

		c.Request = c.Request.WithContext(ctx)
		c.Next()

		if errors := c.Errors; len(errors) > 0 {
			c.JSON(http.StatusGatewayTimeout, gin.H{
				"error": gin.H{"code": "REQUEST_TIMEOUT"},
			})
		}
	}
}
