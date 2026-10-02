package app

import (
	"errors"

	"sshtunnel/internal/config"
	"sshtunnel/internal/tunnel"
)

// Две кнопки главного экрана. Обе едут через одно и то же SSH-соединение с
// сервером, но нужны они по разным поводам:
//
//   - большая — обход блокировок: весь трафик (или выбранные программы) идёт
//     через сервер;
//   - маленькая — сеть устройств: компьютеры видят друг друга по адресам
//     198.19.x.y и именам .mesh, в том числе по RDP.
//
// Выключить обход и оставить сеть — обычное желание: сайты пусть грузятся
// напрямую, а до домашнего компьютера достучаться всё равно надо. Поэтому
// туннель живёт, пока включена хотя бы одна из двух, и гаснет целиком, только
// когда выключены обе.

// ModeLayer — слой VPN, который умеет на ходу переходить между «весь трафик»
// и «только сеть устройств» (маршруты, DNS). Необязательное расширение
// NetLayer: старые реализации без него просто не различают режимы.
type ModeLayer interface {
	SetBypass(on bool)
}

// sysProxyArgs — с чем был включён системный прокси при подключении.
type sysProxyArgs struct {
	cfg                 config.Config
	p                   config.Profile
	httpAddr, socksAddr string
}

// errNoMesh — маленькую кнопку нажали, а сети устройств у этого сервера нет.
var errNoMesh = errors.New("сеть устройств не настроена для этого сервера: включи её в настройках сервера")

func (a *App) bypassOn() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.bypassOff
}

// applyModes переносит выбор кнопок на только что собранный туннель.
func (a *App) applyModes(tun *tunnel.Tunnel) {
	a.mu.Lock()
	bypassOff, meshOff := a.bypassOff, a.meshOff
	a.mu.Unlock()
	tun.SetBypass(!bypassOff)
	tun.SetMeshPaused(meshOff)
}

// resetModes — после полной остановки следующее «Подключить» снова включает
// и обход, и сеть устройств: так кнопка вела себя всегда.
func (a *App) resetModes() {
	a.mu.Lock()
	a.bypassOff, a.meshOff = false, false
	a.mu.Unlock()
}

// meshConfigured — у активного сервера включена сеть устройств.
func (a *App) meshConfigured() bool {
	p := a.Config().Active()
	return p.MeshEnabled && p.MeshKey != ""
}

// Modes — что сейчас включено: обход блокировок, сеть устройств и есть ли
// она вообще у этого сервера. Выключенный туннель — оба «нет».
type Modes struct {
	Bypass         bool `json:"bypass"`
	Mesh           bool `json:"mesh"`
	MeshConfigured bool `json:"meshConfigured"`
}

func (a *App) Modes() Modes {
	a.mu.Lock()
	running, bypassOff, meshOff := a.running, a.bypassOff, a.meshOff
	a.mu.Unlock()
	m := Modes{MeshConfigured: a.meshConfigured()}
	if running {
		m.Bypass = !bypassOff
		m.Mesh = m.MeshConfigured && !meshOff
	}
	return m
}

// SetBypass — большая кнопка. Выключение при включённой сети устройств
// оставляет туннель ради неё; без сети — обычное «Отключить».
func (a *App) SetBypass(on bool) error {
	a.mu.Lock()
	running, tun, netLayer := a.running, a.tun, a.net
	meshActive := !a.meshOff
	a.mu.Unlock()

	if !running {
		if !on {
			return nil
		}
		a.mu.Lock()
		a.bypassOff = false
		a.mu.Unlock()
		return a.Start()
	}

	if !on && !(meshActive && a.meshConfigured()) {
		a.Stop()
		return nil
	}

	a.mu.Lock()
	a.bypassOff = !on
	args := a.sysProxyArgs
	sysOn := a.sysOn
	a.mu.Unlock()

	if tun != nil {
		tun.SetBypass(on)
	}
	if ml, ok := netLayer.(ModeLayer); ok {
		ml.SetBypass(on)
	}
	if netLayer == nil {
		switch {
		case on && !sysOn && args != nil:
			a.enableSysProxy(args.cfg, args.p, args.httpAddr, args.socksAddr)
		case !on && sysOn:
			a.disableSysProxy()
		}
	}
	if on {
		a.Bus.Infof("Обход блокировок включён")
	} else {
		a.Bus.Infof("Обход блокировок выключен — сайты идут напрямую, сеть устройств работает")
	}
	return nil
}

// SetMesh — маленькая кнопка «Сеть». Включение при выключенном туннеле
// поднимает его только ради сети устройств, без обхода блокировок.
func (a *App) SetMesh(on bool) error {
	if on && !a.meshConfigured() {
		return errNoMesh
	}
	a.mu.Lock()
	running, tun, bypassOff := a.running, a.tun, a.bypassOff
	a.mu.Unlock()

	if !running {
		if !on {
			return nil
		}
		a.mu.Lock()
		a.bypassOff, a.meshOff = true, false
		a.mu.Unlock()
		if err := a.Start(); err != nil {
			a.resetModes()
			return err
		}
		a.Bus.Infof("Сеть устройств включена (обход блокировок выключен)")
		return nil
	}

	if !on && bypassOff {
		// Обход и так выключен — без сети туннелю незачем жить.
		a.Stop()
		return nil
	}

	a.mu.Lock()
	a.meshOff = !on
	a.mu.Unlock()
	if tun != nil {
		tun.SetMeshPaused(!on)
	}
	if on {
		a.Bus.Infof("Сеть устройств включена")
	} else {
		a.Bus.Infof("Сеть устройств выключена — обход блокировок работает")
	}
	return nil
}

// disableSysProxy снимает системный прокси, не трогая туннель.
func (a *App) disableSysProxy() {
	if err := a.sys.Disable(); err != nil {
		a.Bus.Errorf("Не удалось вернуть настройки прокси: %v. Проверь: Параметры → Сеть и Интернет → Прокси-сервер", err)
		return
	}
	a.mu.Lock()
	a.sysOn = false
	a.mu.Unlock()
	a.Bus.Infof("Системный прокси выключен")
}
