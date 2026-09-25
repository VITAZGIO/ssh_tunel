package panel

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaxStartupsFor(t *testing.T) {
	cases := map[int]string{
		1:   "10:30:100", // не хуже умолчаний sshd
		3:   "12:30:100",
		30:  "120:30:240",
		250: "1000:30:2000",
	}
	for n, want := range cases {
		if got := MaxStartupsFor(n); got != want {
			t.Errorf("MaxStartupsFor(%d) = %s, ожидалось %s", n, got, want)
		}
	}
}

func TestPlanCapacityСтавитБлокВНачалоИГаситЧужие(t *testing.T) {
	main := sshdFile{Path: "/main", Text: "Include /etc/ssh/sshd_config.d/*.conf\nMaxStartups 10:30:60\nPort 22\n"}
	drop := []sshdFile{
		{Path: "/d/00-hardening.conf", Text: "PasswordAuthentication no\n  maxstartups 5\n"},
		{Path: "/d/50-cloud-init.conf", Text: "PasswordAuthentication yes\n"},
	}
	changed := planCapacity(main, drop, 30)
	if len(changed) != 2 {
		t.Fatalf("ожидалось 2 изменённых файла, получено %d: %+v", len(changed), changed)
	}
	m := changed[0].Text
	if !strings.HasPrefix(m, capacityBegin+"\n") {
		t.Fatalf("блок не в начале файла:\n%s", m)
	}
	if !strings.Contains(m, "\nMaxStartups 120:30:240\n") {
		t.Fatalf("нет нужного значения:\n%s", m)
	}
	if !strings.Contains(m, disabledPrefix+"MaxStartups 10:30:60") {
		t.Fatalf("прежняя строка не погашена:\n%s", m)
	}
	if changed[1].Path != "/d/00-hardening.conf" || !strings.Contains(changed[1].Text, disabledPrefix+"maxstartups 5") {
		t.Fatalf("строка в дополнительном файле не погашена: %+v", changed[1])
	}

	// Повторно с тем же числом — ничего не меняется.
	again := planCapacity(changed[0], []sshdFile{changed[1], drop[1]}, 30)
	if len(again) != 0 {
		t.Fatalf("повторное применение что-то поменяло: %+v", again)
	}

	// Другое число — меняется только наш блок, без второй копии.
	other := planCapacity(changed[0], []sshdFile{changed[1], drop[1]}, 50)
	if len(other) != 1 || strings.Count(other[0].Text, capacityBegin) != 1 ||
		!strings.Contains(other[0].Text, "MaxStartups 200:30:400") {
		t.Fatalf("смена числа устройств: %+v", other)
	}
}

// fakeSSHD подменяет вызовы sshd на время теста.
func fakeSSHD(t *testing.T, check error) *int {
	t.Helper()
	oldCheck, oldReload, oldMain, oldDir := sshdCheck, sshdReload, SSHDPath, SSHDDropInDir
	t.Cleanup(func() { sshdCheck, sshdReload, SSHDPath, SSHDDropInDir = oldCheck, oldReload, oldMain, oldDir })
	reloads := new(int)
	sshdCheck = func(string) error { return check }
	sshdReload = func() error { *reloads++; return nil }
	dir := t.TempDir()
	SSHDPath = filepath.Join(dir, "sshd_config")
	SSHDDropInDir = filepath.Join(dir, "sshd_config.d")
	os.MkdirAll(SSHDDropInDir, 0o755)
	return reloads
}

func TestApplyCapacityПишетИПеречитывает(t *testing.T) {
	reloads := fakeSSHD(t, nil)
	os.WriteFile(SSHDPath, []byte("Port 22\n"), 0o600)
	drop := filepath.Join(SSHDDropInDir, "00-hardening.conf")
	os.WriteFile(drop, []byte("MaxStartups 10:30:100\n"), 0o644)

	if err := ApplyCapacity(30); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(SSHDPath)
	if !strings.Contains(string(data), "MaxStartups 120:30:240") {
		t.Fatalf("значение не записано:\n%s", data)
	}
	if info, _ := os.Stat(SSHDPath); info.Mode().Perm() != 0o600 {
		t.Fatalf("права файла изменились: %v", info.Mode())
	}
	d, _ := os.ReadFile(drop)
	if !strings.HasPrefix(string(d), disabledPrefix) {
		t.Fatalf("чужая строка не погашена: %s", d)
	}
	if *reloads != 1 {
		t.Fatalf("sshd перечитан %d раз", *reloads)
	}

	// Ничего не поменялось — sshd не дёргаем.
	if err := ApplyCapacity(30); err != nil {
		t.Fatal(err)
	}
	if *reloads != 1 {
		t.Fatal("sshd перечитан без изменений")
	}
}

func TestApplyCapacityОткатПриОшибкеПроверки(t *testing.T) {
	reloads := fakeSSHD(t, errors.New("плохой конфиг"))
	const mainText = "Port 22\nMaxStartups 10:30:100\n"
	const dropText = "MaxStartups 5\n"
	os.WriteFile(SSHDPath, []byte(mainText), 0o600)
	drop := filepath.Join(SSHDDropInDir, "10-x.conf")
	os.WriteFile(drop, []byte(dropText), 0o644)

	if err := ApplyCapacity(30); err == nil {
		t.Fatal("ошибка проверки должна вернуться")
	}
	if data, _ := os.ReadFile(SSHDPath); string(data) != mainText {
		t.Fatalf("основной файл не восстановлен:\n%s", data)
	}
	if data, _ := os.ReadFile(drop); string(data) != dropText {
		t.Fatalf("дополнительный файл не восстановлен:\n%s", data)
	}
	if *reloads != 0 {
		t.Fatal("sshd перечитан после неудачной проверки")
	}
}

func TestApplyCapacityГраницы(t *testing.T) {
	fakeSSHD(t, nil)
	for _, n := range []int{0, -1, MaxMaxDevices + 1} {
		if err := ApplyCapacity(n); err == nil {
			t.Errorf("ApplyCapacity(%d) принято", n)
		}
	}
}

func TestSettingsStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	st, err := OpenSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Get().MaxDevices != 0 {
		t.Fatal("пустые настройки должны давать значение по умолчанию")
	}
	if err := st.Update(func(s *Settings) error { s.MaxDevices = 42; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(s *Settings) error { s.MaxDevices = 7; return errors.New("нет") }); err == nil {
		t.Fatal("ошибка fn потерялась")
	}
	again, err := OpenSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Get().MaxDevices; got != 42 {
		t.Fatalf("после перечитывания %d, ожидалось 42", got)
	}
}

// Тот же путь с настоящим sshd, если он есть: конфиг с нашим блоком в начале
// и Include проходит sshd -t, а sshd -T показывает именно наше значение, хотя
// дополнительный файл объявлял своё.
func TestApplyCapacityНастоящийSSHD(t *testing.T) {
	realCheck := sshdCheck
	dir := t.TempDir()
	dropDir := filepath.Join(dir, "d")
	os.MkdirAll(dropDir, 0o755)
	mainPath := filepath.Join(dir, "sshd_config")
	os.WriteFile(mainPath, []byte("Include "+dropDir+"/*.conf\nPort 2222\nMatch Group nobody\n    AllowTcpForwarding yes\n"), 0o644)
	os.WriteFile(filepath.Join(dropDir, "00-hardening.conf"), []byte("MaxStartups 10:30:100\nPasswordAuthentication no\n"), 0o644)
	if err := realCheck(mainPath); err != nil {
		t.Skipf("sshd недоступен или не может проверить конфиг здесь: %v", err)
	}

	fakeSSHD(t, nil)
	sshdCheck = realCheck
	SSHDPath, SSHDDropInDir = mainPath, dropDir
	if err := ApplyCapacity(30); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sshd", "-T", "-f", mainPath).CombinedOutput()
	if err != nil {
		t.Skipf("sshd -T здесь не работает: %v %s", err, out)
	}
	if !strings.Contains(string(out), "\nmaxstartups 120:30:240\n") {
		t.Fatalf("sshd применяет не наше значение:\n%s", out)
	}
}
