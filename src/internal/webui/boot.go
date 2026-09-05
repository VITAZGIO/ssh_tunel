// Автозапуск при включении машины. Раньше это было ручной вознёй из
// README — на Linux положить файл службы systemd и включить её, на Windows
// добавить программу в автозагрузку через реестр. Теперь и то и другое —
// одна и та же галочка «Запускать при старте системы»: что именно она делает
// внутри, зависит от системы (см. boot_linux.go и boot_windows.go), а страница
// об этом не знает и знать не должна.
package webui

import "errors"

// errNeedRoot — команду не пустили без пароля. Актуально только для Linux
// (loginctl enable-linger требует root); страница по этому признаку
// показывает поле для пароля, а не просто ругательство в углу.
var errNeedRoot = errors.New("нужен пароль sudo")

type bootState struct {
	// Supported — есть ли на этой системе вообще способ прописать автозапуск.
	Supported bool `json:"supported"`
	// Enabled — автозапуск включён.
	Enabled bool `json:"enabled"`
	// Linger — служба стартует при загрузке машины, не дожидаясь входа
	// пользователя. Понятие чисто Linux/systemd: на Windows автозапуск через
	// реестр и так срабатывает при входе пользователя, без отдельной ручки.
	Linger   bool   `json:"linger,omitempty"`
	UnitPath string `json:"unitPath,omitempty"`
}

func currentBootState() bootState {
	if !platformBootSupported() {
		return bootState{}
	}
	return bootState{
		Supported: true,
		Enabled:   platformBootEnabled(),
		Linger:    platformBootLinger(),
		UnitPath:  platformUnitPath(),
	}
}

// applyBoot включает или выключает автозапуск. password нужен только на
// Linux и только для sudo — он не сохраняется, не пишется в журнал и не
// возвращается обратно. На Windows автозапуск через реестр правит только
// собственные настройки текущего пользователя, поэтому пароль там не нужен
// вовсе.
func applyBoot(enable bool, password string, flags []string) error {
	if !platformBootSupported() {
		return errors.New("автозапуск при старте системы не поддерживается на этой системе")
	}
	return platformSetBoot(enable, password, flags)
}
