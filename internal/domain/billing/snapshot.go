package billing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

// SnapshotDigest authenticates JSON content independent of PostgreSQL key ordering.
func SnapshotDigest(raw []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", errors.New("snapshot contains trailing data")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

// VerifySnapshots binds both entitlement and document facts to the signed quote.
func (q *Quote) VerifySnapshots(plan, document []byte) error {
	first, err := SnapshotDigest(plan)
	if err != nil || q.PlanSnapshotDigest == "" || first != q.PlanSnapshotDigest {
		return ErrQuoteTampered
	}
	second, err := SnapshotDigest(document)
	if err != nil || q.DocumentSnapshotDigest == "" || second != q.DocumentSnapshotDigest {
		return ErrQuoteTampered
	}
	return nil
}
