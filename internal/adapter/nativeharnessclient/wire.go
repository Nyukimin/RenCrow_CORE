package nativeharnessclient

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// appendLP appends LP(s): the uint64 big-endian byte length of the UTF-8
// string followed by its bytes. The string is used exactly as given.
func appendLP(dst []byte, s string) []byte {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(s)))
	dst = append(dst, length[:]...)
	return append(dst, s...)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
