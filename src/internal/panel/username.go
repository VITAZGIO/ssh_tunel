package panel

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
)

// usernamePrefix — общий префикс unix-пользователей клиентов панели. По нему
// же клиента узнают в системе: "кто такой tun_a1b2c3d4" сразу читается как
// «клиент панели», в отличие от общего пользователя tunnel из ручной
// настройки (docs/SERVER_SETUP.md).
const usernamePrefix = "tun_"

// validUsername — то, что панель готова передать в useradd/usermod/userdel и
// подставить в путь до домашней папки. Строго lowercase-hex после префикса:
// само имя всегда генерирует панель (см. GenerateUsername), но проверка
// остаётся отдельным шагом перед любым системным вызовом — вызывающий код не
// должен полагаться на то, что значение пришло из доверенного места.
var validUsername = regexp.MustCompile(`^tun_[0-9a-f]{16}$`)

// ValidUsername проверяет имя unix-пользователя клиента панели по строгому
// шаблону. Всё, что не проходит эту проверку, panel/provision.go отказывается
// передавать дальше в useradd, usermod, userdel или использовать в пути к
// домашней папке — независимо от того, откуда имя взялось.
func ValidUsername(name string) bool {
	return validUsername.MatchString(name)
}

// GenerateUsername придумывает unix-имя нового клиента: tun_ плюс 16 hex-
// символов из crypto/rand — этого достаточно, чтобы не столкнуться с уже
// существующим пользователем, и достаточно коротко, чтобы уложиться в
// ограничение Linux на длину имени пользователя (32 байта).
func GenerateUsername() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	name := usernamePrefix + hex.EncodeToString(b)
	if !ValidUsername(name) {
		// Не должно случиться никогда — но если формат вдруг разъедется
		// с регэкспом выше, лучше упасть здесь, а не отдать в useradd имя,
		// которое сам же ValidUsername потом завернёт.
		return "", errors.New("сгенерированное имя не прошло собственную проверку")
	}
	return name, nil
}

// ClientID — идентификатор клиента в хранилище и в конфиге share.Doc
// (ТЗ-10). Он же используется как unix-имя, чтобы не городить второй
// генератор случайных строк и не путать «имя клиента в системе» и
// «идентификатор клиента в панели» — это одно и то же значение.
func ClientID(username string) string { return username }
