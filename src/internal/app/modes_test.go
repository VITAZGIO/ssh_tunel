package app

import (
	"testing"

	"sshtunnel/internal/config"
)

// meshVPNApp — приложение в режиме VPN с сервером, у которого включена сеть
// устройств. Сам meshd подставной сервер не умеет — клиенту сети это не
// мешает существовать, а здесь проверяются только кнопки.
func meshVPNApp(t *testing.T, layer *fakeNetLayer, withMesh bool) *App {
	t.Helper()
	keyPath, pub := testKeyPath(t)
	addr := newFakeSSHServer(t, pub, true)
	p := profileAt(t, "main", addr, keyPath)
	if withMesh {
		p.MeshEnabled, p.MeshKey = true, "ключ-сети"
	}
	a := New(config.Config{Profiles: []config.Profile{p}, ActiveProfile: p.ID})
	a.SetNetLayer(layer)
	t.Cleanup(a.StopNow)
	return a
}

func wantModes(t *testing.T, a *App, bypass, mesh bool) {
	t.Helper()
	m := a.Modes()
	if m.Bypass != bypass || m.Mesh != mesh {
		t.Fatalf("кнопки: обход=%v сеть=%v, ожидалось обход=%v сеть=%v", m.Bypass, m.Mesh, bypass, mesh)
	}
}

// Большая кнопка выключает только обход: туннель держится ради сети
// устройств, адаптер переходит в режим «только сеть», а не снимается.
func TestBypassOffKeepsMesh(t *testing.T) {
	layer := &fakeNetLayer{}
	a := meshVPNApp(t, layer, true)
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if on, ok := layer.lastBypass(); !ok || !on {
		t.Fatalf("при подключении адаптер должен получить «весь трафик», получил %v (было ли: %v)", on, ok)
	}
	wantModes(t, a, true, true)

	if err := a.SetBypass(false); err != nil {
		t.Fatal(err)
	}
	if !a.Running() {
		t.Fatal("туннель погас, хотя сеть устройств включена")
	}
	if on, _ := layer.lastBypass(); on {
		t.Fatal("адаптер не переведён в режим «только сеть устройств»")
	}
	a.mu.Lock()
	tun := a.tun
	a.mu.Unlock()
	if tun.Bypass() {
		t.Fatal("туннель всё ещё ведёт сайты через сервер")
	}
	wantModes(t, a, false, true)

	// И обратно — без переподключения.
	if err := a.SetBypass(true); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	same := a.tun == tun
	a.mu.Unlock()
	if !same || !tun.Bypass() {
		t.Fatal("обход должен включаться на том же туннеле")
	}
	wantModes(t, a, true, true)
}

// Маленькая кнопка выключает только сеть; когда выключены обе — туннель
// гаснет целиком, а следующее «Подключить» снова включает всё.
func TestMeshOffThenBypassOffStops(t *testing.T) {
	layer := &fakeNetLayer{}
	a := meshVPNApp(t, layer, true)
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if err := a.SetMesh(false); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	tun := a.tun
	a.mu.Unlock()
	if !a.Running() || !tun.MeshPaused() {
		t.Fatal("сеть устройств должна выключиться, а туннель — остаться")
	}
	wantModes(t, a, true, false)

	if err := a.SetBypass(false); err != nil {
		t.Fatal(err)
	}
	if a.Running() {
		t.Fatal("обе кнопки выключены, а туннель работает")
	}
	wantModes(t, a, false, false)

	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	wantModes(t, a, true, true)
}

// Сеть устройств можно включить отдельно: туннель поднимается без обхода.
func TestMeshOnlyStart(t *testing.T) {
	layer := &fakeNetLayer{}
	a := meshVPNApp(t, layer, true)
	if err := a.SetMesh(true); err != nil {
		t.Fatal(err)
	}
	if on, _ := layer.lastBypass(); on {
		t.Fatal("адаптер поднят в режиме «весь трафик», а просили только сеть")
	}
	wantModes(t, a, false, true)

	if err := a.SetMesh(false); err != nil {
		t.Fatal(err)
	}
	if a.Running() {
		t.Fatal("сеть выключена при выключенном обходе — туннель должен погаснуть")
	}
}

// Без сети устройств большая кнопка работает как раньше, а маленькая
// честно отказывает.
func TestNoMeshButtons(t *testing.T) {
	layer := &fakeNetLayer{}
	a := meshVPNApp(t, layer, false)
	if err := a.SetMesh(true); err == nil {
		t.Fatal("ожидалась ошибка: сеть устройств не настроена")
	}
	if a.Running() {
		t.Fatal("туннель поднялся без сети устройств")
	}
	if err := a.SetBypass(true); err != nil {
		t.Fatal(err)
	}
	wantModes(t, a, true, false)
	if err := a.SetBypass(false); err != nil {
		t.Fatal(err)
	}
	if a.Running() {
		t.Fatal("без сети устройств выключение обхода должно останавливать туннель")
	}
}
