// Package share описывает общий формат файла экспорта/импорта одного сервера
// — тот самый JSON, которым обмениваются два человека через "Экспорт" и
// "Импорт" в веб-панели. Формат общий для серверного webui и для Android:
// android/core подключён к этому модулю через replace ("sshtunnel" =>
// "../../src"), поэтому оба конца читают и пишут один и тот же Doc.
//
// Версия 1 уже разослана людям как готовые файлы — читаться она обязана
// всегда, без ошибок и без потери уже присутствовавших в ней полей.
package share

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// CurrentFormat — версия формата, которую пишет Build. Version 2 добавляет
// необязательные поля (Panel, ClientID, DeviceName) поверх version 1 — сама
// структура JSON не меняется, только становится длиннее, поэтому старые
// файлы читаются Parse без каких-либо условий на Format.
const CurrentFormat = 2

// Doc — содержимое файла обмена настройками одного сервера.
type Doc struct {
	Format         int      `json:"sshTunnelExport"`
	Name           string   `json:"name"`
	Flag           string   `json:"flag,omitempty"`
	Host           string   `json:"host"`
	SSHPort        int      `json:"sshPort"`
	User           string   `json:"user"`
	SocksPort      int      `json:"socksPort"`
	HTTPPort       int      `json:"httpPort"`
	PoolSize       int      `json:"poolSize"`
	FilterMode     string   `json:"filterMode"`
	FilterApps     []string `json:"filterApps,omitempty"`
	DirectHosts    []string `json:"directHosts,omitempty"`
	LocalViaTunnel bool     `json:"localViaTunnel"`
	// KeyIncluded — явный флаг, а не просто «есть KeyContents или нет»: так
	// файл без ключа (человек снял галочку при экспорте) не пытается молча
	// сойти за файл с ключом из-за пустой строки.
	KeyIncluded bool   `json:"keyIncluded"`
	KeyContents string `json:"keyContents,omitempty"`

	// Поля версии 2 — все необязательные, в файлах версии 1 их нет.
	Panel      string `json:"panel,omitempty"`      // адрес веб-панели VPS
	ClientID   string `json:"clientId,omitempty"`   // id устройства-клиента
	DeviceName string `json:"deviceName,omitempty"` // человекочитаемое имя устройства
}

// Build собирает файл обмена из заполненного Doc. Format всегда
// проставляется в CurrentFormat — Build всегда пишет актуальную версию
// формата, что бы ни было в переданном значении Format.
func Build(doc Doc) ([]byte, error) {
	doc.Format = CurrentFormat
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("не удалось собрать файл обмена: %w", err)
	}
	return data, nil
}

// Parse разбирает файл обмена (версии 1 или 2) в Doc. Отдельно проверяет,
// что во входных данных вообще есть что разбирать и что результат похож на
// настоящий экспорт ssh_tunnel, а не случайный JSON.
func Parse(data []byte) (Doc, error) {
	if strings.TrimSpace(string(data)) == "" {
		return Doc{}, errors.New("пустой файл: нечего импортировать")
	}
	var doc Doc
	if err := json.Unmarshal(data, &doc); err != nil {
		return Doc{}, fmt.Errorf("не удалось разобрать файл: %w", err)
	}
	if doc.Host == "" {
		return Doc{}, errors.New("в файле нет адреса сервера — это не экспорт из ssh_tunnel")
	}
	return doc, nil
}
