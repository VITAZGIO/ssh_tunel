package webui

import (
	"encoding/xml"
	"strings"
	"testing"
	"unicode/utf16"
)

// Задача автозапуска VPN: схема разбирается, путь с кириллицей и спецсимволами
// не ломает XML, а ограничения Windows по умолчанию (3 дня, батарея) сняты.
func TestBootTaskXML(t *testing.T) {
	exe := `C:\Users\Иван & Co\ssh_tunnel_vpn.exe`
	sid := "S-1-5-21-1-2-3-1001"
	s := bootTaskXML(sid, exe)

	var task struct {
		Triggers struct {
			Logon struct {
				UserID string `xml:"UserId"`
			} `xml:"LogonTrigger"`
		} `xml:"Triggers"`
		Principal struct {
			UserID    string `xml:"UserId"`
			LogonType string `xml:"LogonType"`
			RunLevel  string `xml:"RunLevel"`
		} `xml:"Principals>Principal"`
		Settings struct {
			Limit       string `xml:"ExecutionTimeLimit"`
			NoBattery   bool   `xml:"DisallowStartIfOnBatteries"`
			StopBattery bool   `xml:"StopIfGoingOnBatteries"`
		} `xml:"Settings"`
		Exec struct {
			Command string `xml:"Command"`
			Dir     string `xml:"WorkingDirectory"`
		} `xml:"Actions>Exec"`
	}
	// Объявление encoding="UTF-16" стандартный разборщик без помощи не
	// примет, а сама строка в Go — UTF-8: убираем пролог.
	body := s[strings.Index(s, "?>")+2:]
	if err := xml.Unmarshal([]byte(body), &task); err != nil {
		t.Fatalf("XML задачи не разбирается: %v\n%s", err, s)
	}
	if task.Exec.Command != exe {
		t.Errorf("команда %q, ожидалась %q", task.Exec.Command, exe)
	}
	if task.Exec.Dir != `C:\Users\Иван & Co` {
		t.Errorf("рабочая папка %q", task.Exec.Dir)
	}
	if task.Triggers.Logon.UserID != sid || task.Principal.UserID != sid {
		t.Errorf("пользователь в задаче не тот: %+v / %+v", task.Triggers.Logon, task.Principal)
	}
	if task.Principal.RunLevel != "HighestAvailable" || task.Principal.LogonType != "InteractiveToken" {
		t.Errorf("задача без прав администратора или не в сеансе пользователя: %+v", task.Principal)
	}
	if task.Settings.Limit != "PT0S" || task.Settings.NoBattery || task.Settings.StopBattery {
		t.Errorf("остались ограничения по времени или батарее: %+v", task.Settings)
	}
}

func TestUTF16LE(t *testing.T) {
	b := utf16LE("Aя")
	if len(b) != 6 || b[0] != 0xFF || b[1] != 0xFE {
		t.Fatalf("нет BOM или длина не та: % x", b)
	}
	u := []uint16{uint16(b[2]) | uint16(b[3])<<8, uint16(b[4]) | uint16(b[5])<<8}
	if got := string(utf16.Decode(u)); got != "Aя" {
		t.Fatalf("получилось %q", got)
	}
}
