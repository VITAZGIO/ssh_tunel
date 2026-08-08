// Package hostkey — проверка ключа SSH-сервера вместо InsecureIgnoreHostKey.
//
// Используется схема TOFU (trust on first use), та же, что у обычного ssh:
// первый увиденный ключ сервера запоминается в known_hosts, дальше сверяется.
// Это защищает от подмены сервера по дороге: провайдер (или кто-то в сети)
// может попытаться встать посередине SSH-соединения, и без этой проверки
// подмена прошла бы незаметно — клиент подключился бы к чужому серверу и отдал
// ему весь трафик.
package hostkey

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ErrChanged возвращается, когда ключ сервера отличается от запомненного.
// Это либо переустановка сервера, либо попытка подмены — молча принимать
// такое нельзя, поэтому решение остаётся за человеком.
type ErrChanged struct {
	Host        string
	KnownHosts  string
	Fingerprint string
}

func (e *ErrChanged) Error() string {
	return fmt.Sprintf(
		"ключ сервера %s отличается от запомненного (сейчас %s).\n"+
			"Если ты сам переустанавливал VPS — удали строку про этот адрес из файла:\n  %s\n"+
			"Если нет — соединение могут перехватывать, подключаться нельзя.",
		e.Host, e.Fingerprint, e.KnownHosts)
}

// Callback возвращает HostKeyCallback, который сверяет ключ с known_hosts,
// а при первом подключении запоминает его. onLearn (может быть nil)
// вызывается, когда ключ запоминается впервые — чтобы показать это в интерфейсе.
func Callback(knownHostsPath string, onLearn func(host, fingerprint string)) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)

		if _, err := os.Stat(knownHostsPath); err == nil {
			check, err := knownhosts.New(knownHostsPath)
			if err != nil {
				return fmt.Errorf("не могу прочитать %s: %w", knownHostsPath, err)
			}
			err = check(hostname, remote, key)
			if err == nil {
				return nil
			}
			var kerr *knownhosts.KeyError
			// KeyError с непустым Want означает "хост known, но ключ другой" —
			// это тревожный случай. Пустой Want — хост просто ещё не записан.
			if ok := asKeyError(err, &kerr); ok && len(kerr.Want) == 0 {
				return learn(knownHostsPath, hostname, remote, key, fp, onLearn)
			}
			if ok := asKeyError(err, &kerr); ok {
				return &ErrChanged{Host: hostname, KnownHosts: knownHostsPath, Fingerprint: fp}
			}
			return err
		}

		return learn(knownHostsPath, hostname, remote, key, fp, onLearn)
	}
}

func learn(path, hostname string, remote net.Addr, key ssh.PublicKey, fp string, onLearn func(string, string)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	// Записываем и имя, под которым подключались, и фактический адрес —
	// иначе при подключении по другому имени ключ снова будет "неизвестным".
	addrs := []string{knownhosts.Normalize(hostname)}
	if remote != nil {
		if n := knownhosts.Normalize(remote.String()); n != addrs[0] {
			addrs = append(addrs, n)
		}
	}
	if _, err := f.WriteString(knownhosts.Line(addrs, key) + "\n"); err != nil {
		return err
	}
	if onLearn != nil {
		onLearn(hostname, fp)
	}
	return nil
}

func asKeyError(err error, target **knownhosts.KeyError) bool {
	ke, ok := err.(*knownhosts.KeyError)
	if ok {
		*target = ke
	}
	return ok
}
