package vpnlayer

import (
	"fmt"
	"sync"

	"github.com/xjasonlyu/tun2socks/v2/core/device/iobased"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// adapterGUID — постоянный GUID адаптера. С ним Windows при каждом включении
// узнаёт «ту же самую сеть» и не спрашивает заново, домашняя она или
// общественная.
var adapterGUID = windows.GUID{
	Data1: 0x5ec7a1e1,
	Data2: 0x55b1,
	Data3: 0x4c0e,
	Data4: [8]byte{0x9a, 0x70, 0x73, 0x73, 0x68, 0x74, 0x75, 0x6e},
}

// device — адаптер Wintun в виде, понятном сетевому стеку: по пакету за раз
// на чтение и на запись. Сделано по образцу tun2socks, но со своим GUID и с
// доступом к LUID — он нужен, чтобы настроить адаптеру адреса и маршруты.
type device struct {
	*iobased.Endpoint

	nt   *tun.NativeTun
	luid winipcfg.LUID

	rMu    sync.Mutex
	rSizes []int
	rBufs  [][]byte
	wMu    sync.Mutex
	wBufs  [][]byte
}

func openDevice() (*device, error) {
	t, err := tun.CreateTUNWithRequestedGUID(adapterName, &adapterGUID, adapterMTU)
	if err != nil {
		return nil, fmt.Errorf("создать адаптер Wintun: %w", err)
	}
	nt := t.(*tun.NativeTun)
	d := &device{
		nt:     nt,
		luid:   winipcfg.LUID(nt.LUID()),
		rSizes: make([]int, 1),
		rBufs:  make([][]byte, 1),
		wBufs:  make([][]byte, 1),
	}
	ep, err := iobased.New(d, adapterMTU, 0)
	if err != nil {
		nt.Close()
		return nil, fmt.Errorf("подключить адаптер к стеку: %w", err)
	}
	d.Endpoint = ep
	return d, nil
}

func (d *device) Read(packet []byte) (int, error) {
	d.rMu.Lock()
	defer d.rMu.Unlock()
	d.rBufs[0] = packet
	_, err := d.nt.Read(d.rBufs, d.rSizes, 0)
	return d.rSizes[0], err
}

func (d *device) Write(packet []byte) (int, error) {
	d.wMu.Lock()
	defer d.wMu.Unlock()
	d.wBufs[0] = packet
	return d.nt.Write(d.wBufs, 0)
}

func (d *device) Name() string { return adapterName }

func (d *device) Type() string { return "wintun" }

// Close закрывает адаптер. Вместе с ним Windows убирает и его адреса, и
// маршруты — возвращать сеть как было отдельно не нужно.
func (d *device) Close() {
	defer d.Endpoint.Close()
	_ = d.nt.Close()
}
