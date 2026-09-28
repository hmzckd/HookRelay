package events

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"sort"
)

// RequestHash binds the event type, exact stored payload bytes, and the set of
// target IDs. Length prefixes make the field boundaries unambiguous.
func RequestHash(input Input) [sha256.Size]byte {
	h := sha256.New()
	writeField(h, []byte("hookrelay-idempotency-v1"))
	writeField(h, []byte(input.Type))
	writeField(h, input.Payload)
	ids := append([]string(nil), input.EndpointIDs...)
	sort.Strings(ids)
	for _, id := range ids {
		writeField(h, []byte(id))
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

func writeField(h hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(value)
}
