package io.github.vitazgio.sshtunnel

import android.content.Context
import java.io.File

/**
 * Настройки приложения.
 *
 * Ключ хранится не здесь, а отдельным файлом в личной папке приложения: так
 * ему можно выставить права, при которых его не прочитает никто другой, и он
 * не попадёт в резервную копию вместе с остальными настройками.
 */
class Settings(context: Context) {

    private val prefs = context.getSharedPreferences("settings", Context.MODE_PRIVATE)
    private val filesDir = context.filesDir

    var host: String
        get() = prefs.getString("host", "") ?: ""
        set(v) = prefs.edit().putString("host", v.trim()).apply()

    var sshPort: Int
        get() = prefs.getInt("sshPort", 22)
        set(v) = prefs.edit().putInt("sshPort", v).apply()

    var user: String
        get() = prefs.getString("user", "tunnel") ?: "tunnel"
        set(v) = prefs.edit().putString("user", v.trim()).apply()

    var poolSize: Int
        get() = prefs.getInt("poolSize", 4)
        set(v) = prefs.edit().putInt("poolSize", v).apply()

    /** Список «всегда напрямую»: адреса и сети, которым туннель не нужен. */
    var directHosts: String
        get() = prefs.getString("directHosts", "") ?: ""
        set(v) = prefs.edit().putString("directHosts", v).apply()

    /**
     * Вести ли в туннель и локальную сеть.
     *
     * По умолчанию нет: иначе домашние сервисы перестали бы открываться, пока
     * туннель включён. Включать стоит ровно в одном случае — когда нужна
     * внутренняя сеть самого сервера.
     */
    var localViaTunnel: Boolean
        get() = prefs.getBoolean("localViaTunnel", false)
        set(v) = prefs.edit().putBoolean("localViaTunnel", v).apply()

    /** Режим отбора приложений: all, only или except. */
    var filterMode: String
        get() = prefs.getString("filterMode", "all") ?: "all"
        set(v) = prefs.edit().putString("filterMode", v).apply()

    /** Приложения, к которым относится режим. */
    var filterApps: Set<String>
        get() = prefs.getStringSet("filterApps", emptySet()) ?: emptySet()
        set(v) = prefs.edit().putStringSet("filterApps", v).apply()

    val keyFile: File get() = File(filesDir, "id_ed25519")
    val knownHostsFile: File get() = File(filesDir, "known_hosts")

    val hasKey: Boolean get() = keyFile.exists() && keyFile.length() > 0

    /**
     * Сохраняет закрытый ключ и закрывает его от посторонних.
     *
     * Права важны не для красоты: SSH-клиенты отказываются работать с ключом,
     * который доступен кому-то ещё, и правильно делают.
     */
    fun saveKey(pem: String) {
        val text = pem.trim()
        keyFile.writeText(if (text.endsWith("\n")) text else text + "\n")
        keyFile.setReadable(false, false)
        keyFile.setWritable(false, false)
        keyFile.setReadable(true, true)
        keyFile.setWritable(true, true)
    }

    fun ready(): Boolean = host.isNotBlank() && user.isNotBlank() && hasKey
}
