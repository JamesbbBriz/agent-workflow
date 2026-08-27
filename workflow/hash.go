package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const (
	maxContentBytes         = 128 << 10
	maxReceiptMaterialBytes = 1536 << 10
)

func validateBoundedJSON(label string, value any) error {
	return validateJSONLimit(label, value, maxContentBytes)
}

func validateJSONLimit(label string, value any, limit int) error {
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", label, err)
	}
	if len(body) > limit {
		return fmt.Errorf("%s exceeds %d bytes", label, limit)
	}
	return nil
}

func Digest(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize value: %w", err)
	}
	hash := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func shortID(prefix, hash string) string {
	const shaPrefix = "sha256:"
	if len(hash) >= len(shaPrefix)+20 {
		return prefix + hash[len(shaPrefix):len(shaPrefix)+20]
	}
	return prefix + hash
}
