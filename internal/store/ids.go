package store

import (
	"crypto/sha256"
	"encoding/hex"
)

func deterministicID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return prefix + "-" + hex.EncodeToString(h.Sum(nil))
}
