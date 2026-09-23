/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2019-2026 WireGuard LLC. All Rights Reserved.
 *
 * Взято из golang.zx2c4.com/wireguard/windows/tunnel/firewall и урезано до
 * того, что нужно режиму VPN: запрет DNS мимо адаптера. Свой код — только
 * permitOwnApp (у WireGuard там ещё проверка SID службы, а мы не служба) и
 * EnableDNSGuard.
 */

package wfp

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

type wfpObjectInstaller func(uintptr) error

type baseObjects struct {
	provider windows.GUID
	filters  windows.GUID
}

func createWfpSession() (uintptr, error) {
	sessionDisplayData, err := createWtFwpmDisplayData0("ssh_tunnel", "ssh_tunnel VPN dynamic session")
	if err != nil {
		return 0, wrapErr(err)
	}

	session := wtFwpmSession0{
		displayData:          *sessionDisplayData,
		flags:                cFWPM_SESSION_FLAG_DYNAMIC,
		txnWaitTimeoutInMSec: windows.INFINITE,
	}

	sessionHandle := uintptr(0)

	err = fwpmEngineOpen0(nil, cRPC_C_AUTHN_WINNT, nil, &session, unsafe.Pointer(&sessionHandle))
	if err != nil {
		return 0, wrapErr(err)
	}

	return sessionHandle, nil
}

func registerBaseObjects(session uintptr) (*baseObjects, error) {
	bo := &baseObjects{}
	var err error
	bo.provider, err = windows.GenerateGUID()
	if err != nil {
		return nil, wrapErr(err)
	}
	bo.filters, err = windows.GenerateGUID()
	if err != nil {
		return nil, wrapErr(err)
	}

	//
	// Register provider.
	//
	{
		displayData, err := createWtFwpmDisplayData0("ssh_tunnel", "ssh_tunnel VPN provider")
		if err != nil {
			return nil, wrapErr(err)
		}
		provider := wtFwpmProvider0{
			providerKey: bo.provider,
			displayData: *displayData,
		}
		err = fwpmProviderAdd0(session, &provider, 0)
		if err != nil {
			// TODO: cleanup entire call chain of these if failure?
			return nil, wrapErr(err)
		}
	}

	//
	// Register filters sublayer.
	//
	{
		displayData, err := createWtFwpmDisplayData0("ssh_tunnel filters", "Permissive and blocking filters")
		if err != nil {
			return nil, wrapErr(err)
		}
		sublayer := wtFwpmSublayer0{
			subLayerKey: bo.filters,
			displayData: *displayData,
			providerKey: &bo.provider,
			weight:      ^uint16(0),
		}
		err = fwpmSubLayerAdd0(session, &sublayer, 0)
		if err != nil {
			return nil, wrapErr(err)
		}
	}

	return bo, nil
}

func permitTunInterface(session uintptr, baseObjects *baseObjects, weight uint8, ifLUID uint64) error {
	ifaceCondition := wtFwpmFilterCondition0{
		fieldKey:  cFWPM_CONDITION_IP_LOCAL_INTERFACE,
		matchType: cFWP_MATCH_EQUAL,
		conditionValue: wtFwpConditionValue0{
			_type: cFWP_UINT64,
			value: (uintptr)(unsafe.Pointer(&ifLUID)),
		},
	}

	filter := wtFwpmFilter0{
		providerKey:         &baseObjects.provider,
		subLayerKey:         baseObjects.filters,
		weight:              filterWeight(weight),
		numFilterConditions: 1,
		filterCondition:     (*wtFwpmFilterCondition0)(unsafe.Pointer(&ifaceCondition)),
		action: wtFwpmAction0{
			_type: cFWP_ACTION_PERMIT,
		},
	}

	filterID := uint64(0)

	//
	// #1 Permit outbound IPv4 traffic.
	//
	{
		displayData, err := createWtFwpmDisplayData0("Permit outbound IPv4 traffic on TUN", "")
		if err != nil {
			return wrapErr(err)
		}

		filter.displayData = *displayData
		filter.layerKey = cFWPM_LAYER_ALE_AUTH_CONNECT_V4

		err = fwpmFilterAdd0(session, &filter, 0, &filterID)
		if err != nil {
			return wrapErr(err)
		}
	}

	//
	// #2 Permit inbound IPv4 traffic.
	//
	{
		displayData, err := createWtFwpmDisplayData0("Permit inbound IPv4 traffic on TUN", "")
		if err != nil {
			return wrapErr(err)
		}

		filter.displayData = *displayData
		filter.layerKey = cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4

		err = fwpmFilterAdd0(session, &filter, 0, &filterID)
		if err != nil {
			return wrapErr(err)
		}
	}

	//
	// #3 Permit outbound IPv6 traffic.
	//
	{
		displayData, err := createWtFwpmDisplayData0("Permit outbound IPv6 traffic on TUN", "")
		if err != nil {
			return wrapErr(err)
		}

		filter.displayData = *displayData
		filter.layerKey = cFWPM_LAYER_ALE_AUTH_CONNECT_V6

		err = fwpmFilterAdd0(session, &filter, 0, &filterID)
		if err != nil {
			return wrapErr(err)
		}
	}

	//
	// #4 Permit inbound IPv6 traffic.
	//
	{
		displayData, err := createWtFwpmDisplayData0("Permit inbound IPv6 traffic on TUN", "")
		if err != nil {
			return wrapErr(err)
		}

		filter.displayData = *displayData
		filter.layerKey = cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6

		err = fwpmFilterAdd0(session, &filter, 0, &filterID)
		if err != nil {
			return wrapErr(err)
		}
	}

	runtime.KeepAlive(ifLUID)
	return nil
}

func blockDNS(except []netip.Addr, session uintptr, baseObjects *baseObjects, weightAllow, weightDeny uint8) error {
	if weightDeny >= weightAllow {
		return errors.New("The allow weight must be greater than the deny weight")
	}

	denyConditions := []wtFwpmFilterCondition0{
		{
			fieldKey:  cFWPM_CONDITION_IP_REMOTE_PORT,
			matchType: cFWP_MATCH_EQUAL,
			conditionValue: wtFwpConditionValue0{
				_type: cFWP_UINT16,
				value: uintptr(53),
			},
		},
		{
			fieldKey:  cFWPM_CONDITION_IP_PROTOCOL,
			matchType: cFWP_MATCH_EQUAL,
			conditionValue: wtFwpConditionValue0{
				_type: cFWP_UINT8,
				value: uintptr(cIPPROTO_UDP),
			},
		},
		// Repeat the condition type for logical OR.
		{
			fieldKey:  cFWPM_CONDITION_IP_PROTOCOL,
			matchType: cFWP_MATCH_EQUAL,
			conditionValue: wtFwpConditionValue0{
				_type: cFWP_UINT8,
				value: uintptr(cIPPROTO_TCP),
			},
		},
	}

	filter := wtFwpmFilter0{
		providerKey:         &baseObjects.provider,
		subLayerKey:         baseObjects.filters,
		weight:              filterWeight(weightDeny),
		numFilterConditions: uint32(len(denyConditions)),
		filterCondition:     (*wtFwpmFilterCondition0)(unsafe.Pointer(&denyConditions[0])),
		action: wtFwpmAction0{
			_type: cFWP_ACTION_BLOCK,
		},
	}

	filterID := uint64(0)

	//
	// #1 Block IPv4 outbound DNS.
	//
	{
		displayData, err := createWtFwpmDisplayData0("Block DNS outbound (IPv4)", "")
		if err != nil {
			return wrapErr(err)
		}

		filter.displayData = *displayData
		filter.layerKey = cFWPM_LAYER_ALE_AUTH_CONNECT_V4

		err = fwpmFilterAdd0(session, &filter, 0, &filterID)
		if err != nil {
			return wrapErr(err)
		}
	}

	//
	// #2 Block IPv4 inbound DNS.
	//
	{
		displayData, err := createWtFwpmDisplayData0("Block DNS inbound (IPv4)", "")
		if err != nil {
			return wrapErr(err)
		}

		filter.displayData = *displayData
		filter.layerKey = cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4

		err = fwpmFilterAdd0(session, &filter, 0, &filterID)
		if err != nil {
			return wrapErr(err)
		}
	}

	//
	// #3 Block IPv6 outbound DNS.
	//
	{
		displayData, err := createWtFwpmDisplayData0("Block DNS outbound (IPv6)", "")
		if err != nil {
			return wrapErr(err)
		}

		filter.displayData = *displayData
		filter.layerKey = cFWPM_LAYER_ALE_AUTH_CONNECT_V6

		err = fwpmFilterAdd0(session, &filter, 0, &filterID)
		if err != nil {
			return wrapErr(err)
		}
	}

	//
	// #4 Block IPv6 inbound DNS.
	//
	{
		displayData, err := createWtFwpmDisplayData0("Block DNS inbound (IPv6)", "")
		if err != nil {
			return wrapErr(err)
		}

		filter.displayData = *displayData
		filter.layerKey = cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6

		err = fwpmFilterAdd0(session, &filter, 0, &filterID)
		if err != nil {
			return wrapErr(err)
		}
	}

	allowConditionsV4 := make([]wtFwpmFilterCondition0, 0, len(denyConditions)+len(except))
	allowConditionsV4 = append(allowConditionsV4, denyConditions...)
	for _, ip := range except {
		if !ip.Is4() {
			continue
		}
		allowConditionsV4 = append(allowConditionsV4, wtFwpmFilterCondition0{
			fieldKey:  cFWPM_CONDITION_IP_REMOTE_ADDRESS,
			matchType: cFWP_MATCH_EQUAL,
			conditionValue: wtFwpConditionValue0{
				_type: cFWP_UINT32,
				value: uintptr(binary.BigEndian.Uint32(ip.AsSlice())),
			},
		})
	}

	storedPointers := make([]*wtFwpByteArray16, 0, len(except))
	allowConditionsV6 := make([]wtFwpmFilterCondition0, 0, len(denyConditions)+len(except))
	allowConditionsV6 = append(allowConditionsV6, denyConditions...)
	for _, ip := range except {
		if !ip.Is6() {
			continue
		}
		address := wtFwpByteArray16{byteArray16: ip.As16()}
		allowConditionsV6 = append(allowConditionsV6, wtFwpmFilterCondition0{
			fieldKey:  cFWPM_CONDITION_IP_REMOTE_ADDRESS,
			matchType: cFWP_MATCH_EQUAL,
			conditionValue: wtFwpConditionValue0{
				_type: cFWP_BYTE_ARRAY16_TYPE,
				value: uintptr(unsafe.Pointer(&address)),
			},
		})
		storedPointers = append(storedPointers, &address)
	}

	filter = wtFwpmFilter0{
		providerKey:         &baseObjects.provider,
		subLayerKey:         baseObjects.filters,
		weight:              filterWeight(weightAllow),
		numFilterConditions: uint32(len(allowConditionsV4)),
		filterCondition:     (*wtFwpmFilterCondition0)(unsafe.Pointer(&allowConditionsV4[0])),
		action: wtFwpmAction0{
			_type: cFWP_ACTION_PERMIT,
		},
	}

	filterID = uint64(0)

	//
	// #5 Allow IPv4 outbound DNS.
	//
	if len(allowConditionsV4) > len(denyConditions) {
		displayData, err := createWtFwpmDisplayData0("Allow DNS outbound (IPv4)", "")
		if err != nil {
			return wrapErr(err)
		}

		filter.displayData = *displayData
		filter.layerKey = cFWPM_LAYER_ALE_AUTH_CONNECT_V4

		err = fwpmFilterAdd0(session, &filter, 0, &filterID)
		if err != nil {
			return wrapErr(err)
		}
	}

	//
	// #6 Allow IPv4 inbound DNS.
	//
	if len(allowConditionsV4) > len(denyConditions) {
		displayData, err := createWtFwpmDisplayData0("Allow DNS inbound (IPv4)", "")
		if err != nil {
			return wrapErr(err)
		}

		filter.displayData = *displayData
		filter.layerKey = cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4

		err = fwpmFilterAdd0(session, &filter, 0, &filterID)
		if err != nil {
			return wrapErr(err)
		}
	}

	filter.filterCondition = (*wtFwpmFilterCondition0)(unsafe.Pointer(&allowConditionsV6[0]))
	filter.numFilterConditions = uint32(len(allowConditionsV6))

	//
	// #7 Allow IPv6 outbound DNS.
	//
	if len(allowConditionsV6) > len(denyConditions) {
		displayData, err := createWtFwpmDisplayData0("Allow DNS outbound (IPv6)", "")
		if err != nil {
			return wrapErr(err)
		}

		filter.displayData = *displayData
		filter.layerKey = cFWPM_LAYER_ALE_AUTH_CONNECT_V6

		err = fwpmFilterAdd0(session, &filter, 0, &filterID)
		if err != nil {
			return wrapErr(err)
		}
	}

	//
	// #8 Allow IPv6 inbound DNS.
	//
	if len(allowConditionsV6) > len(denyConditions) {
		displayData, err := createWtFwpmDisplayData0("Allow DNS inbound (IPv6)", "")
		if err != nil {
			return wrapErr(err)
		}

		filter.displayData = *displayData
		filter.layerKey = cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6

		err = fwpmFilterAdd0(session, &filter, 0, &filterID)
		if err != nil {
			return wrapErr(err)
		}
	}

	runtime.KeepAlive(storedPointers)

	return nil
}
