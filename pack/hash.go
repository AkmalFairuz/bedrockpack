package pack

import (
	sha256lib "crypto/sha256"
	"encoding/binary"
	"github.com/google/uuid"
)

func sha256(input []byte) []byte {
	hash := sha256lib.Sum256(input)
	return hash[:]
}

// uuidFromSeed generates a UUID from a given hash and modifies the last 4 bytes using mod.
func uuidFromSeed(hash []byte, mod int) string {
	if len(hash) < 16 {
		// Zero-fill if the hash is too short
		hash = append(hash, make([]byte, 16-len(hash))...)
	}
	var uuidBytes [16]byte
	copy(uuidBytes[:], hash[:16]) // Use the first 16 bytes for the UUID

	// Extract last 4 bytes and convert to int32
	last4Bytes := binary.BigEndian.Uint32(uuidBytes[12:16])
	newLast4Bytes := last4Bytes + uint32(mod)

	// Put the modified value back
	binary.BigEndian.PutUint32(uuidBytes[12:16], newLast4Bytes)

	// Set UUID variant and version (version 4, random UUID structure)
	uuidBytes[6] = (uuidBytes[6] & 0x0F) | (4 << 4) // Set version 4
	uuidBytes[8] = (uuidBytes[8] & 0x3F) | 0x80     // Set variant

	return uuid.UUID(uuidBytes).String()
}
