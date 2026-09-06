package io.github.vitazgio.sshtunnel

import android.content.Context
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.graphics.Path
import android.util.AttributeSet
import android.view.View

/**
 * Флаг страны, нарисованный кодом — не системным эмодзи-шрифтом.
 *
 * То же соображение, что и на компьютере (см. flagSVG в index.html): эмодзи
 * флагов на части устройств вообще не рисуются (показывают буквы кода
 * страны), а на других выглядят по-разному. Свой рисунок одинаков везде.
 * Флаги не претендуют на точность геральдики — задача узнаваемо пометить
 * вкладку сервера, а не нарисовать точную копию.
 */
class FlagView @JvmOverloads constructor(
    context: Context, attrs: AttributeSet? = null
) : View(context, attrs) {

    /** Код страны из [Cities.CODES] либо пусто — тогда рисуется логотип программы. */
    var code: String = ""
        set(v) { field = v; invalidate() }

    private val paint = Paint(Paint.ANTI_ALIAS_FLAG)

    override fun onDraw(canvas: Canvas) {
        val w = width.toFloat()
        val h = height.toFloat()
        if (w <= 0 || h <= 0) return
        if (code.isBlank()) {
            paint.color = Color.parseColor("#4c8dff")
            paint.style = Paint.Style.FILL
            canvas.drawRoundRect(0f, 0f, w, h, h * 0.18f, h * 0.18f, paint)
            paint.color = Color.WHITE
            paint.textAlign = Paint.Align.CENTER
            paint.textSize = h * 0.5f
            paint.style = Paint.Style.FILL
            canvas.drawText("~", w / 2f, h * 0.72f, paint)
            return
        }
        Flags.draw(canvas, paint, code, w, h)
    }
}

/** Рисует флаг заданного кода страны в прямоугольник (0,0)-(w,h). */
object Flags {

    fun draw(canvas: Canvas, paint: Paint, code: String, w: Float, h: Float) {
        paint.style = Paint.Style.FILL
        when (code) {
            "NL" -> bands(canvas, paint, w, h, horizontal = true, "#AE1C28", "#FFFFFF", "#21468B")
            "DE" -> bands(canvas, paint, w, h, horizontal = true, "#000000", "#DD0000", "#FFCE00")
            "FR" -> bands(canvas, paint, w, h, horizontal = false, "#0055A4", "#FFFFFF", "#EF4135")
            "IT" -> bands(canvas, paint, w, h, horizontal = false, "#009246", "#FFFFFF", "#CE2B37")
            "AT" -> bands(canvas, paint, w, h, horizontal = true, "#ED2939", "#FFFFFF", "#ED2939")
            "ES" -> bands(canvas, paint, w, h, horizontal = true, "#AA151B", "#F1BF00", "#AA151B")
            "PL" -> bands(canvas, paint, w, h, horizontal = true, "#FFFFFF", "#DC143C")
            "CZ" -> bands(canvas, paint, w, h, horizontal = true, "#FFFFFF", "#D7141A")
            "LV" -> bands(canvas, paint, w, h, horizontal = true, "#9E3039", "#FFFFFF", "#9E3039")
            "LT" -> bands(canvas, paint, w, h, horizontal = true, "#FDB913", "#006A44", "#C1272D")
            "EE" -> bands(canvas, paint, w, h, horizontal = true, "#0072CE", "#000000", "#FFFFFF")
            "RO" -> bands(canvas, paint, w, h, horizontal = false, "#002B7F", "#FCD116", "#CE1126")
            "BG" -> bands(canvas, paint, w, h, horizontal = true, "#FFFFFF", "#00966E", "#D62612")
            "HK" -> plain(canvas, paint, w, h, "#DE2910")
            "SE" -> nordicCross(canvas, paint, w, h, "#006AA7", "#FECC00")
            "NO" -> nordicCross(canvas, paint, w, h, "#EF2B2D", "#FFFFFF", "#002868")
            "FI" -> nordicCross(canvas, paint, w, h, "#FFFFFF", "#003580")
            "CH" -> swissCross(canvas, paint, w, h)
            "TR" -> turkey(canvas, paint, w, h)
            "AE" -> uae(canvas, paint, w, h)
            "IL" -> israel(canvas, paint, w, h)
            "US" -> usa(canvas, paint, w, h)
            "CA" -> canada(canvas, paint, w, h)
            "BR" -> brazil(canvas, paint, w, h)
            "SG" -> singapore(canvas, paint, w, h)
            "JP" -> japan(canvas, paint, w, h)
            "AU" -> australia(canvas, paint, w, h)
            "GB" -> unionJack(canvas, paint, w, h)
            else -> plain(canvas, paint, w, h, "#828b9e")
        }
    }

    private fun c(hex: String) = Color.parseColor(hex)

    private fun plain(canvas: Canvas, paint: Paint, w: Float, h: Float, color: String) {
        paint.color = c(color)
        canvas.drawRect(0f, 0f, w, h, paint)
    }

    /** Горизонтальные или вертикальные равные полосы — большинство флагов Европы. */
    private fun bands(canvas: Canvas, paint: Paint, w: Float, h: Float, horizontal: Boolean, vararg colors: String) {
        val n = colors.size
        for (i in 0 until n) {
            paint.color = c(colors[i])
            if (horizontal) {
                val y0 = h * i / n
                val y1 = h * (i + 1) / n
                canvas.drawRect(0f, y0, w, y1, paint)
            } else {
                val x0 = w * i / n
                val x1 = w * (i + 1) / n
                canvas.drawRect(x0, 0f, x1, h, paint)
            }
        }
    }

    /** Скандинавский смещённый крест: SE/NO/FI. */
    private fun nordicCross(canvas: Canvas, paint: Paint, w: Float, h: Float, bg: String, cross: String, outline: String? = null) {
        paint.color = c(bg)
        canvas.drawRect(0f, 0f, w, h, paint)
        val vx = w * 0.38f
        val vw = w * 0.16f
        val hy = h * 0.42f
        val hh = h * 0.16f
        if (outline != null) {
            paint.color = c(outline)
            canvas.drawRect(vx - vw * 0.25f, 0f, vx + vw * 1.25f, h, paint)
            canvas.drawRect(0f, hy - hh * 0.25f, w, hy + hh * 1.25f, paint)
        }
        paint.color = c(cross)
        canvas.drawRect(vx, 0f, vx + vw, h, paint)
        canvas.drawRect(0f, hy, w, hy + hh, paint)
    }

    private fun swissCross(canvas: Canvas, paint: Paint, w: Float, h: Float) {
        paint.color = c("#D52B1E")
        canvas.drawRect(0f, 0f, w, h, paint)
        paint.color = Color.WHITE
        canvas.drawRect(w * 0.42f, h * 0.2f, w * 0.58f, h * 0.8f, paint)
        canvas.drawRect(w * 0.2f, h * 0.42f, w * 0.8f, h * 0.58f, paint)
    }

    private fun turkey(canvas: Canvas, paint: Paint, w: Float, h: Float) {
        paint.color = c("#E30A17")
        canvas.drawRect(0f, 0f, w, h, paint)
        paint.color = Color.WHITE
        canvas.drawCircle(w * 0.42f, h * 0.5f, h * 0.28f, paint)
        paint.color = c("#E30A17")
        canvas.drawCircle(w * 0.48f, h * 0.5f, h * 0.22f, paint)
        paint.color = Color.WHITE
        star(canvas, paint, w * 0.62f, h * 0.5f, h * 0.11f)
    }

    private fun uae(canvas: Canvas, paint: Paint, w: Float, h: Float) {
        bands(canvas, paint, w, h, horizontal = true, "#00732F", "#FFFFFF", "#000000")
        paint.color = c("#FF0000")
        canvas.drawRect(0f, 0f, w * 0.28f, h, paint)
    }

    private fun israel(canvas: Canvas, paint: Paint, w: Float, h: Float) {
        paint.color = Color.WHITE
        canvas.drawRect(0f, 0f, w, h, paint)
        paint.color = c("#0038B8")
        canvas.drawRect(0f, h * 0.14f, w, h * 0.24f, paint)
        canvas.drawRect(0f, h * 0.76f, w, h * 0.86f, paint)
        star(canvas, paint, w / 2f, h / 2f, h * 0.2f)
        star(canvas, paint, w / 2f, h / 2f, h * 0.2f, rotated = true)
    }

    private fun usa(canvas: Canvas, paint: Paint, w: Float, h: Float) {
        val stripes = 7
        for (i in 0 until stripes) {
            paint.color = if (i % 2 == 0) c("#B22234") else Color.WHITE
            canvas.drawRect(0f, h * i / stripes, w, h * (i + 1) / stripes, paint)
        }
        paint.color = c("#3C3B6E")
        canvas.drawRect(0f, 0f, w * 0.5f, h * 4f / 7f, paint)
    }

    private fun canada(canvas: Canvas, paint: Paint, w: Float, h: Float) {
        paint.color = c("#D52B1E")
        canvas.drawRect(0f, 0f, w * 0.26f, h, paint)
        canvas.drawRect(w * 0.74f, 0f, w, h, paint)
        paint.color = Color.WHITE
        canvas.drawRect(w * 0.26f, 0f, w * 0.74f, h, paint)
        paint.color = c("#D52B1E")
        canvas.drawCircle(w / 2f, h / 2f, h * 0.16f, paint)
    }

    private fun brazil(canvas: Canvas, paint: Paint, w: Float, h: Float) {
        paint.color = c("#009C3B")
        canvas.drawRect(0f, 0f, w, h, paint)
        paint.color = c("#FFDF00")
        val path = Path()
        path.moveTo(w / 2f, h * 0.14f)
        path.lineTo(w * 0.88f, h / 2f)
        path.lineTo(w / 2f, h * 0.86f)
        path.lineTo(w * 0.12f, h / 2f)
        path.close()
        canvas.drawPath(path, paint)
        paint.color = c("#002776")
        canvas.drawCircle(w / 2f, h / 2f, h * 0.19f, paint)
    }

    private fun singapore(canvas: Canvas, paint: Paint, w: Float, h: Float) {
        paint.color = c("#EF3340")
        canvas.drawRect(0f, 0f, w, h / 2f, paint)
        paint.color = Color.WHITE
        canvas.drawRect(0f, h / 2f, w, h, paint)
        paint.color = Color.WHITE
        canvas.drawCircle(w * 0.24f, h * 0.28f, h * 0.19f, paint)
        paint.color = c("#EF3340")
        canvas.drawCircle(w * 0.30f, h * 0.28f, h * 0.16f, paint)
        paint.color = Color.WHITE
        star(canvas, paint, w * 0.4f, h * 0.28f, h * 0.08f)
    }

    private fun japan(canvas: Canvas, paint: Paint, w: Float, h: Float) {
        paint.color = Color.WHITE
        canvas.drawRect(0f, 0f, w, h, paint)
        paint.color = c("#BC002D")
        canvas.drawCircle(w / 2f, h / 2f, h * 0.3f, paint)
    }

    private fun australia(canvas: Canvas, paint: Paint, w: Float, h: Float) {
        paint.color = c("#00247D")
        canvas.drawRect(0f, 0f, w, h, paint)
        paint.color = Color.WHITE
        canvas.drawRect(0f, h * 0.4f, w * 0.5f, h * 0.6f, paint)
        canvas.drawRect(w * 0.2f, 0f, w * 0.3f, h, paint)
        for ((sx, sy) in listOf(0.7f to 0.24f, 0.85f to 0.5f, 0.7f to 0.76f, 0.92f to 0.82f)) {
            star(canvas, paint, w * sx, h * sy, h * 0.06f)
        }
    }

    /** Упрощённый Union Jack: не геральдически точный, но узнаваемый. */
    private fun unionJack(canvas: Canvas, paint: Paint, w: Float, h: Float) {
        paint.color = c("#00247D")
        canvas.drawRect(0f, 0f, w, h, paint)
        paint.color = Color.WHITE
        canvas.drawRect(0f, h * 0.4f, w, h * 0.6f, paint)
        canvas.drawRect(w * 0.4f, 0f, w * 0.6f, h, paint)
        paint.color = c("#CF142B")
        canvas.drawRect(0f, h * 0.45f, w, h * 0.55f, paint)
        canvas.drawRect(w * 0.45f, 0f, w * 0.55f, h, paint)
    }

    private fun star(canvas: Canvas, paint: Paint, cx: Float, cy: Float, r: Float, rotated: Boolean = false) {
        val path = Path()
        val startAngle = if (rotated) -90.0 else -90.0 + 60.0
        for (i in 0..2) {
            val a1 = Math.toRadians(startAngle + i * 120.0)
            val a2 = Math.toRadians(startAngle + i * 120.0 + 60.0)
            if (i == 0) {
                path.moveTo(cx + r * Math.cos(a1).toFloat(), cy + r * Math.sin(a1).toFloat())
            }
            path.lineTo(cx + r * Math.cos(a2).toFloat(), cy + r * Math.sin(a2).toFloat())
            val a3 = Math.toRadians(startAngle + (i + 1) * 120.0)
            path.lineTo(cx + r * Math.cos(a3).toFloat(), cy + r * Math.sin(a3).toFloat())
        }
        path.close()
        canvas.drawPath(path, paint)
    }
}

/** Список городов для быстрого выбора вкладки сервера — тот же, что на компьютере. */
object Cities {
    data class City(val nameRu: String, val nameEn: String, val code: String)

    val ALL = listOf(
        City("Амстердам", "Amsterdam", "NL"),
        City("Франкфурт", "Frankfurt", "DE"),
        City("Берлин", "Berlin", "DE"),
        City("Лондон", "London", "GB"),
        City("Париж", "Paris", "FR"),
        City("Мадрид", "Madrid", "ES"),
        City("Милан", "Milan", "IT"),
        City("Цюрих", "Zurich", "CH"),
        City("Вена", "Vienna", "AT"),
        City("Стокгольм", "Stockholm", "SE"),
        City("Осло", "Oslo", "NO"),
        City("Хельсинки", "Helsinki", "FI"),
        City("Варшава", "Warsaw", "PL"),
        City("Прага", "Prague", "CZ"),
        City("Рига", "Riga", "LV"),
        City("Вильнюс", "Vilnius", "LT"),
        City("Таллин", "Tallinn", "EE"),
        City("Бухарест", "Bucharest", "RO"),
        City("София", "Sofia", "BG"),
        City("Стамбул", "Istanbul", "TR"),
        City("Дубай", "Dubai", "AE"),
        City("Тель-Авив", "Tel Aviv", "IL"),
        City("Нью-Йорк", "New York", "US"),
        City("Лос-Анджелес", "Los Angeles", "US"),
        City("Торонто", "Toronto", "CA"),
        City("Сан-Паулу", "São Paulo", "BR"),
        City("Сингапур", "Singapore", "SG"),
        City("Токио", "Tokyo", "JP"),
        City("Гонконг", "Hong Kong", "HK"),
        City("Сидней", "Sydney", "AU"),
    )

    fun label(city: City, lang: String): String = if (lang == "en") city.nameEn else city.nameRu

    /** Город из имени и кода флага профиля, либо null (своё имя без города). */
    fun forProfile(name: String, flag: String): City? =
        ALL.find { it.code == flag && (it.nameRu == name || it.nameEn == name) }
}
