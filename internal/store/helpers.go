package store

import (
	"github.com/jackc/pgx/v5/pgtype"
)

// pgtypeUUID parses a string into a pgtype.UUID.
func pgtypeUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	_ = u.Scan(s)
	return u
}

// pgtypeText wraps a string in a pgtype.Text.
func pgtypeText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}

// uuidToString converts a pgtype.UUID to its string form.
func uuidToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return u.String()
}
