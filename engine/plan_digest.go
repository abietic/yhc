package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

const planDigestPrefix = "sha256:"

// PlanBytesDigest returns the canonical identity for exact Plan file bytes.
// It intentionally does not normalize text, newlines, paths, or metadata.
func PlanBytesDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return planDigestPrefix + hex.EncodeToString(digest[:])
}

// ReadPlanReviewSnapshot returns the exact bytes and canonical digest an
// approval adapter must render before it can approve an ExitPlanMode request.
func ReadPlanReviewSnapshot(path string) ([]byte, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read Plan review snapshot: %w", err)
	}
	return data, PlanBytesDigest(data), nil
}

func validPlanDigest(value string) bool {
	if value != strings.TrimSpace(value) ||
		!strings.HasPrefix(value, planDigestPrefix) {
		return false
	}
	encoded := strings.TrimPrefix(value, planDigestPrefix)
	if encoded != strings.ToLower(encoded) {
		return false
	}
	decoded, err := hex.DecodeString(encoded)
	return err == nil && len(decoded) == sha256.Size
}
