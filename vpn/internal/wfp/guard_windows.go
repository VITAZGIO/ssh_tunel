// Пакет wfp ставит фильтры брандмауэра Windows (Windows Filtering Platform),
// которые не дают DNS-запросам уйти мимо адаптера VPN.
//
// Зачем. Windows рассылает DNS-запросы сразу через все сетевые карты
// («умное» разрешение имён на нескольких интерфейсах) и берёт первый ответ.
// Без запрета запрос уходил бы и в провайдерский DNS: провайдер видел бы,
// какие сайты открываются, а на заблокированные имена он отвечает адресом
// заглушки — и иногда успевал бы ответить раньше нас.
//
// Почему именно так. Фильтры ставятся в «динамической» сессии: Windows
// удаляет их сама, как только процесс завершился — в том числе аварийно.
// Если бы программа прописывала DNS где-то постоянно (реестр, правила NRPT),
// её падение оставило бы компьютер без DNS вовсе.
package wfp

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Guard — поставленные фильтры. Снимаются Close или завершением процесса.
type Guard struct {
	session uintptr
}

// Веса фильтров внутри нашего подслоя: больший вес побеждает.
const (
	weightPermit = 15 // своя программа и всё, что идёт через адаптер
	weightDeny   = 14 // запрет DNS
)

// EnableDNSGuard запрещает DNS (порт 53, UDP и TCP) на всех интерфейсах,
// кроме адаптера с данным LUID. Своей программе разрешено всё: ей нужно
// разрешать имена «напрямую» (домашняя сеть, список исключений) у настоящего
// DNS-сервера.
func EnableDNSGuard(tunLUID uint64) (*Guard, error) {
	session, err := createWfpSession()
	if err != nil {
		return nil, err
	}
	err = runTransaction(session, func(session uintptr) error {
		base, err := registerBaseObjects(session)
		if err != nil {
			return err
		}
		if err := permitOwnApp(session, base, weightPermit); err != nil {
			return err
		}
		if err := permitTunInterface(session, base, weightPermit, tunLUID); err != nil {
			return err
		}
		return blockDNS(nil, session, base, weightPermit, weightDeny)
	})
	if err != nil {
		fwpmEngineClose0(session)
		return nil, err
	}
	return &Guard{session: session}, nil
}

// Close снимает фильтры. Повторный вызов ничего не делает.
func (g *Guard) Close() {
	if g == nil || g.session == 0 {
		return
	}
	fwpmEngineClose0(g.session)
	g.session = 0
}

// permitOwnApp разрешает всё процессу с тем же exe, что у нас.
func permitOwnApp(session uintptr, base *baseObjects, weight uint8) error {
	appID, err := getCurrentProcessAppID()
	if err != nil {
		return err
	}
	defer fwpmFreeMemory0(unsafe.Pointer(&appID))

	condition := wtFwpmFilterCondition0{
		fieldKey:  cFWPM_CONDITION_ALE_APP_ID,
		matchType: cFWP_MATCH_EQUAL,
		conditionValue: wtFwpConditionValue0{
			_type: cFWP_BYTE_BLOB_TYPE,
			value: uintptr(unsafe.Pointer(appID)),
		},
	}
	filter := wtFwpmFilter0{
		providerKey:         &base.provider,
		subLayerKey:         base.filters,
		weight:              filterWeight(weight),
		flags:               cFWPM_FILTER_FLAG_CLEAR_ACTION_RIGHT,
		numFilterConditions: 1,
		filterCondition:     &condition,
		action:              wtFwpmAction0{_type: cFWP_ACTION_PERMIT},
	}

	layers := []struct {
		name string
		key  windows.GUID
	}{
		{"Permit ssh_tunnel outbound (IPv4)", cFWPM_LAYER_ALE_AUTH_CONNECT_V4},
		{"Permit ssh_tunnel inbound (IPv4)", cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4},
		{"Permit ssh_tunnel outbound (IPv6)", cFWPM_LAYER_ALE_AUTH_CONNECT_V6},
		{"Permit ssh_tunnel inbound (IPv6)", cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6},
	}
	for _, l := range layers {
		displayData, err := createWtFwpmDisplayData0(l.name, "")
		if err != nil {
			return err
		}
		filter.displayData = *displayData
		filter.layerKey = l.key
		var filterID uint64
		if err := fwpmFilterAdd0(session, &filter, 0, &filterID); err != nil {
			return wrapErr(err)
		}
	}
	runtime.KeepAlive(&condition)
	return nil
}
