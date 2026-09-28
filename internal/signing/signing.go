package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

const Tolerance = 5 * time.Minute

// Sign authenticates the exact body bytes and routing metadata.
func Sign(secret []byte, timestamp int64, keyID, eventID, eventType, deliveryID, attemptID string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	for _, field := range []string{strconv.FormatInt(timestamp, 10), keyID, eventID, eventType, deliveryID, attemptID} {
		mac.Write([]byte(field))
		mac.Write([]byte("."))
	}
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func Verify(secret []byte, now time.Time, timestampText, keyID, eventID, eventType, deliveryID, attemptID string, body []byte, signature string) bool {
	if len(secret) < 32 || keyID == "" || eventID == "" || eventType == "" || deliveryID == "" || attemptID == "" || !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil || strconv.FormatInt(timestamp, 10) != timestampText {
		return false
	}
	delta := now.Sub(time.Unix(timestamp, 0))
	if delta < -Tolerance || delta > Tolerance {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	expected, _ := hex.DecodeString(strings.TrimPrefix(Sign(secret, timestamp, keyID, eventID, eventType, deliveryID, attemptID, body), "sha256="))
	return subtle.ConstantTimeCompare(provided, expected) == 1
}
