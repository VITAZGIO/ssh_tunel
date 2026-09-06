package panel

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"

	"golang.org/x/crypto/ssh"
)

// generateKeyPair создаёт новую пару ed25519, ту же, что и локальная панель
// (см. internal/webui/genkey.go) — тот же формат и тот же способ сборки, но
// без записи на диск: ключ клиента панели живёт только в ClientStore, пока
// клиента не удалили.
func generateKeyPair() (pubLine, privPEM string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	return line, string(pem.EncodeToMemory(block)), nil
}
