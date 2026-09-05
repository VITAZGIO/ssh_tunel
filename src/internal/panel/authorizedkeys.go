package panel

import (
	"errors"
	"strings"
)

// sshOptions — опции OpenSSH перед ключом в authorized_keys, которые
// разрешают проброс портов и запрещают всё остальное, до чего можно было бы
// дотянуться по SSH (интерактивную сессию, X11, агент). "restrict" сначала
// выключает вообще всё, что умеет опция, а port-forwarding включает обратно
// только проброс — так набор безопасен и от новых возможностей, которые
// когда-нибудь добавят в OpenSSH: они по умолчанию окажутся выключены, а не
// включены.
//
// Сессия и остальное дополнительно закрыты в sshd_config блоком
// "Match Group sshtunnel" (см. sshd.go) — он одной строкой действует на всех
// клиентов сразу, а restrict,port-forwarding здесь — второй, независимый
// слой той же защиты на случай, если Match-блок в sshd_config кто-то уберёт
// или отредактирует.
const sshOptions = "restrict,port-forwarding"

// AuthorizedKeysLine собирает единственную строку authorized_keys для
// клиента панели: опции, сам ключ и id клиента комментарием в конце — тем же
// полем OpenSSH, которое обычно используется для "user@host". По этому
// комментарию строка потом находится обратно при заморозке/разморозке
// клиента (ТЗ-09), не полагаясь на то, что во всём файле она вообще одна.
//
// pubKey — открытый ключ в формате "ssh-ed25519 AAAA..." (как отдаёт
// ssh.MarshalAuthorizedKey, без опций и без комментария) — сюда нельзя
// подставлять произвольный текст: перевод строки внутри pubKey превратил бы
// одну запись authorized_keys в две с уже своими, не проверенными опциями.
func AuthorizedKeysLine(pubKey, clientID string) (string, error) {
	pubKey = strings.TrimSpace(pubKey)
	if pubKey == "" {
		return "", errors.New("пустой открытый ключ")
	}
	if strings.ContainsAny(pubKey, "\n\r") {
		return "", errors.New("открытый ключ не должен содержать перевод строки")
	}
	if !ValidUsername(clientID) {
		return "", errors.New("некорректный id клиента")
	}
	return sshOptions + " " + pubKey + " " + clientID, nil
}

// AuthorizedKeyEntry — одна разобранная строка authorized_keys.
type AuthorizedKeyEntry struct {
	Options  string
	KeyType  string
	KeyData  string
	Comment  string
	Raw      string
	IsClient bool // true, если строка похожа на запись, собранную AuthorizedKeysLine
}

// ParseAuthorizedKeys разбирает содержимое файла authorized_keys построчно.
// Пустые строки и строки-комментарии (начинающиеся с "#") сохраняются как
// есть, чтобы RenderAuthorizedKeys могла собрать файл обратно один в один
// для всего, что панель не трогает.
func ParseAuthorizedKeys(content string) []AuthorizedKeyEntry {
	var entries []AuthorizedKeyEntry
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			entries = append(entries, AuthorizedKeyEntry{Raw: line})
			continue
		}
		entries = append(entries, parseAuthorizedKeyLine(line))
	}
	return entries
}

// parseAuthorizedKeyLine разбирает одну непустую строку ключа. Опции — это
// всё, что стоит перед известным типом ключа: authorized_keys не отделяет их
// собственным символом, поэтому граница ищется по первому полю, похожему на
// тип ключа OpenSSH.
func parseAuthorizedKeyLine(line string) AuthorizedKeyEntry {
	fields := strings.Fields(line)
	keyTypeAt := -1
	for i, f := range fields {
		if isKeyType(f) {
			keyTypeAt = i
			break
		}
	}
	if keyTypeAt == -1 {
		// Не похоже на строку с ключом вовсе — сохраняем как есть, чтобы
		// ничего не потерять и не сломать файл при пересборке.
		return AuthorizedKeyEntry{Raw: line}
	}
	e := AuthorizedKeyEntry{
		Options: strings.Join(fields[:keyTypeAt], " "),
		KeyType: fields[keyTypeAt],
		Raw:     line,
	}
	if keyTypeAt+1 < len(fields) {
		e.KeyData = fields[keyTypeAt+1]
	}
	if keyTypeAt+2 < len(fields) {
		e.Comment = strings.Join(fields[keyTypeAt+2:], " ")
	}
	e.IsClient = e.Options == sshOptions && ValidUsername(e.Comment)
	return e
}

func isKeyType(s string) bool {
	switch s {
	case "ssh-ed25519", "ssh-rsa", "ssh-dss",
		"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
		"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com":
		return true
	}
	return false
}

// RenderAuthorizedKeys собирает содержимое файла обратно из записей,
// сохраняя формат панели: "опции ключ комментарий" по одной строке. Для
// строк, разобранных как есть (Raw без разбора), используется исходный
// текст.
//
// Склеивает строки только через "\n", без своего завершающего перевода
// строки: ParseAuthorizedKeys уже кладёт пустую запись последним элементом
// для любого содержимого, которое заканчивалось на "\n" (так всегда
// заканчивается strings.Split), поэтому завершающий перевод строки
// восстанавливается сам, и Parse+Render — это ровно обратные друг другу
// операции.
func RenderAuthorizedKeys(entries []AuthorizedKeyEntry) string {
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.KeyType == "" {
			lines = append(lines, e.Raw)
			continue
		}
		line := e.KeyType + " " + e.KeyData
		if e.Options != "" {
			line = e.Options + " " + line
		}
		if e.Comment != "" {
			line = line + " " + e.Comment
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// RemoveClientKey убирает из содержимого authorized_keys строку данного
// клиента (по комментарию-id), оставляя всё остальное без изменений.
// Используется при заморозке клиента (ТЗ-09): сам ключ остаётся в
// хранилище панели, из authorized_keys пропадает только эта строка — sshd
// перестаёт его принимать, не трогая ничего вокруг.
func RemoveClientKey(content, clientID string) string {
	entries := ParseAuthorizedKeys(content)
	kept := entries[:0]
	for _, e := range entries {
		if e.IsClient && e.Comment == clientID {
			continue
		}
		kept = append(kept, e)
	}
	return RenderAuthorizedKeys(kept)
}

// HasClientKey — есть ли в содержимом authorized_keys действующая строка
// этого клиента. Используется, чтобы отличить «заморожен» (ключа в файле
// нет) от «активен» без отдельного флага состояния, который легко забыть
// обновить в паре с самим файлом.
func HasClientKey(content, clientID string) bool {
	for _, e := range ParseAuthorizedKeys(content) {
		if e.IsClient && e.Comment == clientID {
			return true
		}
	}
	return false
}
