package handlers

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
)

func aiTokenMetadata(reserved int64, actual pgtype.Int8) gin.H {
	fields := gin.H{"reserved_tokens": reserved, "reserved_tokens_exact": strconv.FormatInt(reserved, 10), "actual_tokens": nil, "actual_tokens_exact": nil}
	if reserved > 9007199254740991 {
		fields["reserved_tokens"] = nil
	}
	if actual.Valid {
		fields["actual_tokens_exact"] = strconv.FormatInt(actual.Int64, 10)
		if actual.Int64 <= 9007199254740991 {
			fields["actual_tokens"] = actual.Int64
		}
	}
	return fields
}
