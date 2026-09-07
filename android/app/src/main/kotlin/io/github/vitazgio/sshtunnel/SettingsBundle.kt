package io.github.vitazgio.sshtunnel

import org.json.JSONArray
import org.json.JSONObject

/**
 * Перенос ВСЕХ настроек одним текстом: серверы с ключами, выбор приложений,
 * общие галочки и белый список рекламы. Смысл — отдать человеку (маме, другу)
 * одно сообщение в мессенджере, после вставки которого у него всё работает
 * так же, как у тебя.
 *
 * Формат тот же, что у компьютерной версии (src/internal/share/bundle.go):
 * объект с полем "sshTunnelBackup" и массивом "servers", каждый элемент
 * которого — обычный экспорт одного сервера. Сами элементы собирает и
 * разбирает ядро на Go (mobile.buildConfig/parseConfig), поэтому телефон и
 * компьютер не могут разойтись в мелочах формата.
 *
 * Списки блокировки рекламы намеренно НЕ переносятся: в них десятки тысяч
 * строк, и текст перестал бы влезать в сообщение. Переносятся ссылки на
 * источники (по ним список скачается заново) и белый список исключений — он
 * маленький и без него на новом устройстве снова ломались бы те же сайты.
 */
object SettingsBundle {

    const val FORMAT = 1

    class BadBundle(message: String) : Exception(message)

    /** Собирает выгрузку всех настроек в текст для буфера обмена. */
    fun build(settings: Settings): String {
        val servers = JSONArray()
        val list = settings.profiles
        var active = -1
        for ((i, p) in list.withIndex()) {
            val keyFile = settings.keyFile(p.id)
            val key = if (keyFile.exists() && keyFile.length() > 0) keyFile.readText() else ""
            val doc = mobile.Mobile.buildConfig(
                p.name, p.flag, p.host, p.sshPort.toLong(), p.user, p.poolSize.toLong(),
                p.filterMode, p.filterApps.joinToString("\n"), p.directHosts,
                p.localViaTunnel, key.isNotBlank(), key, p.panel, p.deviceName,
            )
            servers.put(JSONObject(doc))
            if (p.id == settings.activeProfileId) active = i
        }

        val mobileSection = JSONObject()
            .put("adBlockEnabled", settings.adBlockEnabled)
            .put("adBlockSources", settings.adBlockSources)
            .put("adBlockAllowlist", settings.adBlockAllowlist)
            .put("udpRelayEnabled", settings.udpRelayEnabled)

        return JSONObject()
            .put("sshTunnelBackup", FORMAT)
            .put("servers", servers)
            .put("active", active)
            .put("language", settings.language)
            .put("showServerPicker", settings.showServerPicker)
            .put("mobile", mobileSection)
            .toString(2)
    }

    /**
     * Применяет вставленную выгрузку, ЗАМЕНЯЯ всё, что было. Именно заменяя:
     * смысл кнопки — «сделай у меня так же», а дописывание чужих серверов к
     * своим дало бы список, в котором не разобраться.
     *
     * Возвращает число серверов, которые приехали.
     */
    fun apply(settings: Settings, text: String): Int {
        val trimmed = text.trim()
        if (trimmed.isEmpty()) throw BadBundle("empty")
        val root = try {
            JSONObject(trimmed)
        } catch (e: Exception) {
            throw BadBundle("not json")
        }
        if (!root.has("sshTunnelBackup")) {
            // Частая ошибка: сюда вставляют экспорт ОДНОГО сервера. Отличаем
            // его явно, чтобы подсказать нужную кнопку, а не сказать «мусор».
            if (root.has("sshTunnelExport")) throw BadBundle("one server")
            throw BadBundle("not a bundle")
        }
        val servers = root.optJSONArray("servers") ?: JSONArray()
        if (servers.length() == 0) throw BadBundle("no servers")

        // Старые серверы вместе с их ключами убираем только после того, как
        // разобрали новые: если во вставленном тексте окажется мусор, у
        // человека должно остаться то, что было.
        val parsed = ArrayList<Pair<Settings.Profile, String>>()
        for (i in 0 until servers.length()) {
            val doc = mobile.Mobile.parseConfig(servers.getJSONObject(i).toString())
            val p = Settings.Profile(
                id = "", name = doc.getName(), flag = doc.getFlag(), host = doc.getHost(),
                sshPort = doc.getSshPort().toInt().let { if (it > 0) it else 22 },
                user = doc.getUser().ifBlank { "tunnel" },
                poolSize = doc.getPoolSize().toInt().let { if (it > 0) it else 4 },
                directHosts = doc.getDirectHosts(),
                localViaTunnel = doc.getLocalViaTunnel(),
                filterMode = doc.getFilterMode().ifBlank { "all" },
                filterApps = doc.getFilterApps().split("\n")
                    .map { it.trim() }.filter { it.isNotBlank() }.toMutableSet(),
                panel = doc.getPanel(), deviceName = doc.getDeviceName(),
            )
            val key = if (doc.getKeyIncluded()) doc.getKeyContents() else ""
            parsed.add(p to key)
        }

        settings.replaceProfiles(parsed, root.optInt("active", 0))

        root.optString("language").takeIf { it.isNotBlank() }?.let { settings.language = it }
        settings.showServerPicker = root.optBoolean("showServerPicker", settings.showServerPicker)
        root.optJSONObject("mobile")?.let { m ->
            settings.adBlockEnabled = m.optBoolean("adBlockEnabled", settings.adBlockEnabled)
            settings.adBlockSources = m.optString("adBlockSources", settings.adBlockSources)
            settings.adBlockAllowlist = m.optString("adBlockAllowlist", settings.adBlockAllowlist)
            settings.udpRelayEnabled = m.optBoolean("udpRelayEnabled", settings.udpRelayEnabled)
        }
        return parsed.size
    }
}
