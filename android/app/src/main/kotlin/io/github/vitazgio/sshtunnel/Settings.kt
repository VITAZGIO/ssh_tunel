package io.github.vitazgio.sshtunnel

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.util.UUID

/**
 * Настройки приложения: список серверов (профилей) и общие параметры.
 *
 * Модель повторяет компьютерную версию (src/internal/config/config.go) —
 * список профилей плюс id активного, вместо одного плоского набора полей.
 * У каждого профиля свой файл ключа в filesDir/keys — так смена активного
 * сервера не путает ключи между собой.
 *
 * Старый (версии 1) набор плоских настроек хранится в тех же
 * SharedPreferences под старыми именами ключей — при первом обращении после
 * обновления он превращается в единственный профиль, а файл ключа
 * id_ed25519 переезжает в личный файл этого профиля. Дальше эти плоские
 * ключи не используются.
 */
class Settings(context: Context) {

    data class Profile(
        var id: String,
        var name: String,
        var flag: String,
        var host: String,
        var sshPort: Int,
        var user: String,
        var poolSize: Int,
        var directHosts: String,
        var localViaTunnel: Boolean,
        var filterMode: String,
        var filterApps: MutableSet<String>,
        // panel/deviceName — заполняются только при импорте конфига,
        // выданного веб-панелью на VPS (internal/share, поля версии 2).
        // Сервер, настроенный руками, их не имеет — panel остаётся пустым,
        // и экран настроек ничего дополнительного не показывает.
        var panel: String = "",
        var deviceName: String = "",
    )

    private val prefs = context.getSharedPreferences("settings", Context.MODE_PRIVATE)
    private val filesDir = context.filesDir

    private fun newProfile(name: String, flag: String = ""): Profile = Profile(
        id = UUID.randomUUID().toString(),
        name = name, flag = flag, host = "", sshPort = 22, user = "tunnel", poolSize = 4,
        directHosts = "", localViaTunnel = false, filterMode = "all", filterApps = mutableSetOf(),
    )

    /**
     * Список серверов. Если он ещё пуст (первый запуск после обновления или
     * совсем новая установка) — заводится один профиль: из старых плоских
     * настроек, если они были, иначе просто пустой.
     */
    var profiles: MutableList<Profile>
        get() {
            val raw = prefs.getString("profiles", null)
            val list = if (raw != null) decodeProfiles(raw) else mutableListOf()
            if (list.isEmpty()) {
                val migrated = migrateLegacy()
                list.add(migrated)
                saveAll(list, migrated.id)
            }
            return list
        }
        private set(_) {}

    var activeProfileId: String
        get() = prefs.getString("activeProfileId", "") ?: ""
        private set(v) = prefs.edit().putString("activeProfileId", v).apply()

    /** Сервер, который сейчас используется для подключения. */
    fun active(): Profile {
        val list = profiles
        return list.find { it.id == activeProfileId } ?: list.first()
    }

    /** Делает сервер активным. Ничего не значащий id — без эффекта. */
    fun setActive(id: String) {
        if (profiles.any { it.id == id }) activeProfileId = id
    }

    fun addProfile(name: String, flag: String): Profile {
        val list = profiles
        val p = newProfile(name.ifBlank { "Server ${list.size + 1}" }, flag)
        list.add(p)
        saveAll(list, activeProfileId.ifBlank { p.id })
        return p
    }

    /** Последний сервер удалить нельзя — подключаться должно быть куда. */
    fun removeProfile(id: String): Boolean {
        val list = profiles
        if (list.size <= 1) return false
        if (!list.removeAll { it.id == id }) return false
        keyFile(id).delete()
        val active = if (activeProfileId == id) list.first().id else activeProfileId
        saveAll(list, active)
        return true
    }

    fun saveProfile(p: Profile) {
        val list = profiles
        val i = list.indexOfFirst { it.id == p.id }
        if (i >= 0) list[i] = p else list.add(p)
        saveAll(list, activeProfileId.ifBlank { p.id })
    }

    private fun saveAll(list: List<Profile>, active: String) {
        prefs.edit()
            .putString("profiles", encodeProfiles(list))
            .putString("activeProfileId", active)
            .apply()
    }

    private fun encodeProfiles(list: List<Profile>): String {
        val arr = JSONArray()
        for (p in list) {
            val o = JSONObject()
            o.put("id", p.id); o.put("name", p.name); o.put("flag", p.flag)
            o.put("host", p.host); o.put("sshPort", p.sshPort); o.put("user", p.user)
            o.put("poolSize", p.poolSize); o.put("directHosts", p.directHosts)
            o.put("localViaTunnel", p.localViaTunnel); o.put("filterMode", p.filterMode)
            val apps = JSONArray()
            p.filterApps.forEach { apps.put(it) }
            o.put("filterApps", apps)
            o.put("panel", p.panel); o.put("deviceName", p.deviceName)
            arr.put(o)
        }
        return arr.toString()
    }

    private fun decodeProfiles(raw: String): MutableList<Profile> {
        val out = mutableListOf<Profile>()
        try {
            val arr = JSONArray(raw)
            for (i in 0 until arr.length()) {
                val o = arr.getJSONObject(i)
                val apps = mutableSetOf<String>()
                o.optJSONArray("filterApps")?.let { a ->
                    for (j in 0 until a.length()) apps.add(a.getString(j))
                }
                out.add(
                    Profile(
                        id = o.optString("id").ifBlank { UUID.randomUUID().toString() },
                        name = o.optString("name", "Server"),
                        flag = o.optString("flag", ""),
                        host = o.optString("host", ""),
                        sshPort = o.optInt("sshPort", 22),
                        user = o.optString("user", "tunnel"),
                        poolSize = o.optInt("poolSize", 4),
                        directHosts = o.optString("directHosts", ""),
                        localViaTunnel = o.optBoolean("localViaTunnel", false),
                        filterMode = o.optString("filterMode", "all"),
                        filterApps = apps,
                        panel = o.optString("panel", ""),
                        deviceName = o.optString("deviceName", ""),
                    )
                )
            }
        } catch (e: Exception) {
            // Повреждённый JSON — лучше начать с чистого листа, чем упасть.
        }
        return out
    }

    /**
     * Превращает старые (версии 1) плоские настройки в первый профиль, а файл
     * ключа id_ed25519 переносит в его личный файл. Если старых настроек не
     * было вовсе (чистая установка) — профиль просто пустой.
     */
    private fun migrateLegacy(): Profile {
        val p = newProfile("Server")
        val legacyHost = prefs.getString("host", null)
        if (legacyHost != null) {
            p.host = legacyHost
            p.sshPort = prefs.getInt("sshPort", 22)
            p.user = prefs.getString("user", "tunnel") ?: "tunnel"
            p.poolSize = prefs.getInt("poolSize", 4)
            p.directHosts = prefs.getString("directHosts", "") ?: ""
            p.localViaTunnel = prefs.getBoolean("localViaTunnel", false)
            p.filterMode = prefs.getString("filterMode", "all") ?: "all"
            p.filterApps = (prefs.getStringSet("filterApps", emptySet()) ?: emptySet()).toMutableSet()
        }
        val legacyKey = File(filesDir, "id_ed25519")
        if (legacyKey.exists() && legacyKey.length() > 0) {
            try {
                legacyKey.copyTo(keyFile(p.id), overwrite = true)
                applyKeyPermissions(keyFile(p.id))
                legacyKey.delete()
            } catch (e: Exception) {
                // Ключ не перенёсся — профиль останется без него, но остальные
                // настройки не теряются, ключ можно будет импортировать заново.
            }
        }
        return p
    }

    fun keyFile(profileId: String): File {
        val dir = File(filesDir, "keys")
        if (!dir.exists()) dir.mkdirs()
        return File(dir, "${profileId}_id_ed25519")
    }

    val knownHostsFile: File get() = File(filesDir, "known_hosts")

    // ---------------------------------------------------------------------
    // Удобные свойства для активного профиля — чтобы TunnelService и старый
    // код экрана, писавшие settings.host/settings.keyFile и т.п., не менялись
    // вовсе: они по-прежнему читают и пишут ровно активный сервер.
    // ---------------------------------------------------------------------

    var host: String
        get() = active().host
        set(v) { val p = active(); p.host = v.trim(); saveProfile(p) }

    var sshPort: Int
        get() = active().sshPort
        set(v) { val p = active(); p.sshPort = v; saveProfile(p) }

    var user: String
        get() = active().user
        set(v) { val p = active(); p.user = v.trim(); saveProfile(p) }

    var poolSize: Int
        get() = active().poolSize
        set(v) { val p = active(); p.poolSize = v; saveProfile(p) }

    var directHosts: String
        get() = active().directHosts
        set(v) { val p = active(); p.directHosts = v; saveProfile(p) }

    var localViaTunnel: Boolean
        get() = active().localViaTunnel
        set(v) { val p = active(); p.localViaTunnel = v; saveProfile(p) }

    var filterMode: String
        get() = active().filterMode
        set(v) { val p = active(); p.filterMode = v; saveProfile(p) }

    var filterApps: Set<String>
        get() = active().filterApps
        set(v) { val p = active(); p.filterApps = v.toMutableSet(); saveProfile(p) }

    val keyFile: File get() = keyFile(active().id)

    val hasKey: Boolean get() = keyFile.exists() && keyFile.length() > 0

    fun saveKey(pem: String) = saveKeyFor(active().id, pem)

    /**
     * Сохраняет закрытый ключ и закрывает его от посторонних.
     *
     * Права важны не для красоты: SSH-клиенты отказываются работать с ключом,
     * который доступен кому-то ещё, и правильно делают.
     */
    fun saveKeyFor(profileId: String, pem: String) {
        val text = pem.trim()
        val f = keyFile(profileId)
        f.writeText(if (text.endsWith("\n")) text else text + "\n")
        applyKeyPermissions(f)
    }

    private fun applyKeyPermissions(f: File) {
        f.setReadable(false, false)
        f.setWritable(false, false)
        f.setReadable(true, true)
        f.setWritable(true, true)
    }

    fun ready(): Boolean = host.isNotBlank() && user.isNotBlank() && hasKey

    // ---------------------------------------------------------------------
    // Язык интерфейса.
    // ---------------------------------------------------------------------

    var language: String
        get() = prefs.getString("language", "ru") ?: "ru"
        set(v) = prefs.edit().putString("language", v).apply()

    // ---------------------------------------------------------------------
    // Свёрнутость блоков настроек: при самом первом открытии экрана оба
    // развёрнуты, дальше по умолчанию свёрнуты — но ручной выбор человека
    // запоминается навсегда, поверх этого умолчания.
    // ---------------------------------------------------------------------

    var settingsEverOpened: Boolean
        get() = prefs.getBoolean("settingsEverOpened", false)
        set(v) = prefs.edit().putBoolean("settingsEverOpened", v).apply()

    var serverPanelExpanded: Boolean
        get() = prefs.getBoolean("serverPanelExpanded", false)
        set(v) = prefs.edit().putBoolean("serverPanelExpanded", v).apply()

    var generalPanelExpanded: Boolean
        get() = prefs.getBoolean("generalPanelExpanded", false)
        set(v) = prefs.edit().putBoolean("generalPanelExpanded", v).apply()
}
