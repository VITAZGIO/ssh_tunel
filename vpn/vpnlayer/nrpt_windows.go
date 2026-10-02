package vpnlayer

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// Имена .mesh в режиме «только сеть устройств». DNS системы при этом смотрит
// в настоящую сеть, а не в наш адаптер, — и спросить у нас «ноутбук.mesh»
// Windows сама не догадалась бы. Для таких случаев в Windows есть таблица
// политик разрешения имён (NRPT): «имена в зоне .mesh спрашивай у
// 198.18.0.53». Так же делают Tailscale и NetBird для своих доменов.
//
// Правило — ключ в реестре, за которым следит служба DNS-клиента Windows.
// Ключ свой и с постоянным именем: при выключении и при следующем запуске
// после аварии его легко найти и убрать, не задев чужие правила.

const (
	nrptBase = `SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig`
	nrptKey  = nrptBase + `\{5ec7a1e1-55b1-4c0e-9a70-6d6573680000}`
	// nrptGenericDNS — NRPT_CONFIG_OPTION_GENERIC_DNS: правило задаёт свой
	// DNS-сервер для зоны.
	nrptGenericDNS = 0x8
)

func setMeshDNSRule() error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, nrptKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return errors.Join(
		k.SetDWordValue("Version", 2),
		k.SetStringsValue("Name", []string{".mesh"}),
		k.SetStringValue("GenericDNSServers", dnsAddr.String()),
		k.SetDWordValue("ConfigOptions", nrptGenericDNS),
		k.SetStringValue("Comment", "ssh_tunnel VPN: сеть устройств"),
		k.SetStringValue("DisplayName", "ssh_tunnel .mesh"),
		k.SetStringValue("IPSECCARestriction", ""),
	)
}

func removeMeshDNSRule() {
	_ = registry.DeleteKey(registry.LOCAL_MACHINE, nrptKey)
}
