package hostkey

import (
	"crypto"

	"golang.org/x/crypto/ssh"
)

// Небольшие псевдонимы, чтобы тест не тащил импорт ssh в каждую функцию.
type sshPublicKey = ssh.PublicKey

func newPublicKey(key crypto.PublicKey) (ssh.PublicKey, error) { return ssh.NewPublicKey(key) }
