package webui

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ensureKey — то, что раньше было двумя ручными шагами в терминале
// («создать ключ», «показать открытый ключ»), одним вызовом. Если по пути
// уже лежит ключ — он не трогается, а его открытая часть просто читается с
// диска; если ключа нет — создаётся новый, ed25519, без пароля, теми же
// параметрами, что и `ssh-keygen -t ed25519 -N ""`. Внешний ssh-keygen не
// вызывается: формат приватного ключа собирает сам golang.org/x/crypto/ssh,
// это тот же формат, что использует настоящий ssh-keygen.
func ensureKey(path string) (pubLine string, created bool, err error) {
	path, err = expandHome(path)
	if err != nil {
		return "", false, err
	}
	if path == "" {
		return "", false, errors.New("не указан путь к ключу")
	}

	_, privErr := os.Stat(path)
	pubData, pubErr := os.ReadFile(path + ".pub")
	switch {
	case privErr == nil && pubErr == nil:
		return strings.TrimSpace(string(pubData)), false, nil
	case privErr == nil && pubErr != nil:
		return "", false, fmt.Errorf(
			"приватный ключ %s уже есть, а файла %s.pub рядом нет — проверь вручную", path, path)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", false, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, fmt.Errorf("не могу создать папку для ключа: %w", err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", false, fmt.Errorf("не могу записать приватный ключ: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", false, err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if err := os.WriteFile(path+".pub", []byte(line+"\n"), 0o644); err != nil {
		return "", false, fmt.Errorf("не могу записать открытый ключ: %w", err)
	}
	return line, true, nil
}

// expandHome разворачивает "~" в начале пути — поле в форме принимает любой
// текст, а ssh-агенты и стандартная библиотека сами "~" не разворачивают.
func expandHome(path string) (string, error) {
	if path == "" || path[0] != '~' {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}
