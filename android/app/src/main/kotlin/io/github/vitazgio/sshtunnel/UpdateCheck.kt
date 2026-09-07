package io.github.vitazgio.sshtunnel

import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

/**
 * Проверка обновлений по странице релизов GitHub — то же поведение, что у
 * компьютерной версии (src/internal/updater, задача в docs/UPDATE_SPEC.md).
 *
 * Запрос делает приложение, а не ядро на Go: ядру доступ в интернет мимо
 * туннеля не нужен. Само приложение себя не устанавливает — Android не даст
 * этого без отдельного разрешения на установку пакетов, — поэтому в лучшем
 * случае APK кладётся в «Загрузки», а ставит его человек.
 */
object UpdateCheck {

    private const val API =
        "https://api.github.com/repos/VITAZGIO/ssh_tunel/releases/latest"

    /** Что показать человеку после нажатия кнопки. */
    data class Result(
        val installed: String,
        val latest: String,
        val newer: Boolean,
        val ahead: Boolean,
        val releaseUrl: String,
        val apkUrl: String,
    )

    class RateLimited : Exception("rate limited")

    /** Ходит в сеть — вызывать только из фонового потока. */
    fun check(installed: String): Result {
        val conn = (URL(API).openConnection() as HttpURLConnection).apply {
            requestMethod = "GET"
            connectTimeout = 10_000
            readTimeout = 10_000
            setRequestProperty("Accept", "application/vnd.github+json")
            setRequestProperty("User-Agent", "ssh_tunnel/$installed (android)")
        }
        try {
            val code = conn.responseCode
            if (code == 403 && conn.getHeaderField("X-RateLimit-Remaining") == "0") {
                throw RateLimited()
            }
            if (code != 200) throw Exception("GitHub: HTTP $code")
            val root = JSONObject(conn.inputStream.bufferedReader().use { it.readText() })
            val tag = root.optString("tag_name")
            if (tag.isBlank()) throw Exception("GitHub did not name a version")

            var apk = ""
            val assets = root.optJSONArray("assets")
            if (assets != null) {
                for (i in 0 until assets.length()) {
                    val a = assets.getJSONObject(i)
                    if (a.optString("name") == "ssh_tunnel.apk") {
                        apk = a.optString("browser_download_url"); break
                    }
                }
            }
            val cmp = compare(installed, tag)
            return Result(
                installed = installed,
                latest = tag.removePrefix("v"),
                newer = cmp < 0,
                ahead = cmp > 0,
                releaseUrl = root.optString("html_url"),
                apkUrl = apk,
            )
        } finally {
            conn.disconnect()
        }
    }

    /**
     * Сравнивает версии MAJOR.MINOR.PATCH числами, а не строками: 1.10.0
     * новее 1.9.0, хотя как строка он «меньше». Если разобрать не удалось —
     * 0, то есть «считаем, что одинаковые», и кнопка просто не предложит
     * скачивание.
     */
    fun compare(a: String, b: String): Int {
        val pa = parse(a) ?: return 0
        val pb = parse(b) ?: return 0
        for (i in 0..2) {
            if (pa[i] != pb[i]) return pa[i].compareTo(pb[i])
        }
        return 0
    }

    private fun parse(v: String): IntArray? {
        var text = v.trim().removePrefix("v")
        val cut = text.indexOfFirst { it == '-' || it == '+' }
        if (cut >= 0) text = text.substring(0, cut)
        if (text.isBlank()) return null
        val out = intArrayOf(0, 0, 0)
        val parts = text.split(".")
        if (parts.size > 3) return null
        for ((i, part) in parts.withIndex()) {
            out[i] = part.trim().toIntOrNull() ?: return null
        }
        return out
    }
}
