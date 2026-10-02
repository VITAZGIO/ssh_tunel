// Автозапуск версии с режимом VPN на Windows. Обычному ssh_tunnel.exe хватает
// записи в HKCU\...\Run, а ssh_tunnel_vpn.exe так не поднять: в его манифесте
// requireAdministrator, и Windows при входе в систему такие программы из Run и
// из папки «Автозагрузка» просто не запускает — молча, без окна UAC. Это и было
// «прокси стартует сам, а VPN — нет».
//
// Обходят это так же, как другие VPN-клиенты с окном: задачей в Планировщике
// заданий с флажком «Выполнять с наивысшими правами» и триггером «При входе в
// систему». Задачу создаёт уже повышенный процесс (VPN без прав админа и не
// запустится), поэтому дальше Windows поднимает программу с правами
// администратора сама, без окна UAC — в сеансе пользователя, с окном и значком
// в трее.
//
// Здесь — только текст задачи, без вызовов Windows, чтобы его можно было
// проверить тестом на любой системе. Регистрация — в boot_windows.go.
package webui

import (
	"bytes"
	"encoding/xml"
	"strings"
	"unicode/utf16"
)

// bootTaskName — имя задачи в корне Планировщика. Его видно в «Планировщике
// заданий» (taskschd.msc): по нему её легко найти и удалить руками.
const bootTaskName = "ssh_tunnel VPN"

// bootTaskXML — описание задачи в формате, который понимает
// schtasks /Create /XML. Через XML, а не флагами schtasks, потому что флагами
// не снять ограничения, которые Windows ставит задачам по умолчанию и которые
// для VPN губительны:
//   - «Останавливать задачу через 3 дня» — VPN выключился бы на третий день
//     работы без перезагрузки (ExecutionTimeLimit PT0S — без ограничения);
//   - «Запускать только при питании от сети» и «Останавливать при переходе на
//     батарею» — на ноутбуке без зарядки VPN не поднялся бы вовсе;
//   - приоритет 7 («ниже обычного») — для программы, через которую идёт весь
//     трафик, это лишние задержки.
//
// userSID — тот, при чьём входе запускать и от чьего имени. Именно SID, а не
// имя: имя пользователя можно сменить, SID — нет.
func bootTaskXML(userSID, exe string) string {
	var b bytes.Buffer
	esc := func(s string) string {
		var e bytes.Buffer
		_ = xml.EscapeText(&e, []byte(s))
		return e.String()
	}
	// Папку отрезаем руками, а не filepath.Dir: путь всегда виндовый, а
	// тест гоняется и на Linux, где обратная косая — не разделитель.
	dir := exe
	if i := strings.LastIndexAny(exe, `\/`); i > 0 {
		dir = exe[:i]
	}
	sid, cmd, dir := esc(userSID), esc(exe), esc(dir)

	b.WriteString(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Запускает ssh_tunnel VPN при входе в систему. Создано самой программой (галочка «Запускать при старте системы»).</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + sid + `</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + sid + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>4</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + cmd + `</Command>
      <WorkingDirectory>` + dir + `</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`)
	return b.String()
}

// utf16LE — текст в UTF-16 (little endian) с BOM. В таком виде schtasks
// принимает XML без капризов; UTF-8 с объявлением encoding="UTF-16" он
// отвергает, а без объявления путается на кириллице в пути (C:\Users\Имя\...).
func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 2+2*len(u))
	out[0], out[1] = 0xFF, 0xFE
	for i, c := range u {
		out[2+2*i] = byte(c)
		out[3+2*i] = byte(c >> 8)
	}
	return out
}
