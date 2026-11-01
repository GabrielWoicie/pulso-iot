package identity

import (
	"crypto/rand"
	"fmt"
)

// New uses crypto/rand to create an RFC 4122 version 4 identifier.
func New() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("gerador criptográfico indisponível")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
