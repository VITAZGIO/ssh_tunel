package tunnel

import (
	"context"
	"testing"

	"sshtunnel/internal/events"
	"sshtunnel/internal/mesh"
)

// Выключенный обход блокировок — всё, что не к сети устройств, идёт
// напрямую, как и при сливе.
func TestBypassOffGoesDirect(t *testing.T) {
	tun := New(Config{}, events.NewBus())
	if !tun.Bypass() || !tun.viaServer() {
		t.Fatal("по умолчанию обход включён")
	}
	tun.SetBypass(false)
	if tun.Bypass() || tun.viaServer() {
		t.Fatal("обход выключен, а соединения всё ещё идут через сервер")
	}
	if tun.UDPRelay() != nil {
		t.Fatal("без обхода UDP через сервер не нужен")
	}
	tun.SetBypass(true)
	if !tun.viaServer() {
		t.Fatal("обход включили обратно, а соединения идут напрямую")
	}
}

// Сеть устройств на паузе не поднимается, даже если настроена, и
// поднимается снова, когда паузу снимают при живом пуле.
func TestMeshPause(t *testing.T) {
	tun := New(Config{Mesh: &mesh.Config{Key: "k", DeviceID: "d", Name: "n"}}, events.NewBus())
	if !tun.MeshConfigured() {
		t.Fatal("сеть настроена, а MeshConfigured — нет")
	}
	tun.SetMeshPaused(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tun.startMesh(ctx)
	if tun.Mesh() != nil {
		t.Fatal("сеть на паузе, а клиент поднят")
	}

	tun.mu.Lock()
	tun.poolCtx = ctx
	tun.mu.Unlock()
	tun.SetMeshPaused(false)
	if tun.Mesh() == nil {
		t.Fatal("паузу сняли при живом пуле, а сеть не поднялась")
	}
	tun.SetMeshPaused(true)
	if tun.Mesh() != nil {
		t.Fatal("пауза не погасила сеть")
	}
	cancel()
	tun.wg.Wait()
}
