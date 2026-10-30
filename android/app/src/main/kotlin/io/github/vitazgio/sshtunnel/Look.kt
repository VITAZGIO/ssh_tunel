package io.github.vitazgio.sshtunnel

import android.app.Activity
import android.content.Context
import android.graphics.Canvas
import android.graphics.ColorFilter
import android.graphics.Paint
import android.graphics.Path
import android.graphics.PixelFormat
import android.graphics.Rect
import android.graphics.Typeface
import android.graphics.drawable.Drawable
import android.os.Handler
import android.os.Looper
import android.util.AttributeSet
import android.util.TypedValue
import android.view.View
import androidx.core.content.ContextCompat
import kotlin.random.Random

/**
 * Оформление: тема (оригинал, тёмная, светлая, консоль), фон (цвет, код,
 * автор) и фигурные кнопки главного экрана — то же, что в окне на компьютере.
 */
object Look {
    /** Ставит выбранную тему. Вызывать до setContentView. */
    fun apply(activity: Activity) {
        activity.setTheme(
            when (Settings(activity).theme) {
                "dark" -> R.style.AppTheme_Dark
                "light" -> R.style.AppTheme_Light
                "console" -> R.style.AppTheme_Console
                else -> R.style.AppTheme
            }
        )
    }

    private val ATTR = mapOf(
        R.color.bg to R.attr.c_bg, R.color.card to R.attr.c_card, R.color.card2 to R.attr.c_card2,
        R.color.line to R.attr.c_line, R.color.text to R.attr.c_text, R.color.dim to R.attr.c_dim,
        R.color.accent to R.attr.c_accent, R.color.accent_muted to R.attr.c_accent_muted,
        R.color.ok to R.attr.c_ok, R.color.warn to R.attr.c_warn, R.color.err to R.attr.c_err,
        R.color.logo to R.attr.c_logo,
    )

    /** Цвет палитры с учётом темы; прочие цвета (системные) — как есть. */
    fun color(ctx: Context, res: Int): Int {
        val attr = ATTR[res] ?: return ContextCompat.getColor(ctx, res)
        val tv = TypedValue()
        return if (ctx.theme.resolveAttribute(attr, tv, true)) tv.data else ContextCompat.getColor(ctx, res)
    }
}

/** Короткая запись Look.color для экранов. */
fun Context.tc(res: Int): Int = Look.color(this, res)

/**
 * Фон кнопок главного экрана без скруглений: прямоугольник, кнопка с вырезом
 * под шестиугольник слева или справа, сам шестиугольник и «язычок» вверх.
 * Вырез и острие одной глубины — так соседние края идут параллельно.
 */
class ShapeBg(private val kind: Kind, color: Int, private val depth: Float) : Drawable() {
    enum class Kind { RECT, NOTCH_RIGHT, NOTCH_LEFT, HEX }

    private val paint = Paint(Paint.ANTI_ALIAS_FLAG).apply { this.color = color }
    private val path = Path()

    fun setColor(c: Int) {
        paint.color = c
        invalidateSelf()
    }

    override fun onBoundsChange(b: Rect) {
        val l = b.left.toFloat(); val t = b.top.toFloat()
        val r = b.right.toFloat(); val btm = b.bottom.toFloat()
        val mid = (t + btm) / 2
        path.reset()
        when (kind) {
            Kind.RECT -> path.addRect(l, t, r, btm, Path.Direction.CW)
            Kind.NOTCH_RIGHT -> {
                path.moveTo(l, t); path.lineTo(r, t); path.lineTo(r - depth, mid)
                path.lineTo(r, btm); path.lineTo(l, btm); path.close()
            }
            Kind.NOTCH_LEFT -> {
                path.moveTo(l, t); path.lineTo(r, t); path.lineTo(r, btm)
                path.lineTo(l, btm); path.lineTo(l + depth, mid); path.close()
            }
            Kind.HEX -> {
                path.moveTo(l + depth, t); path.lineTo(r - depth, t); path.lineTo(r, mid)
                path.lineTo(r - depth, btm); path.lineTo(l + depth, btm); path.lineTo(l, mid); path.close()
            }
        }
    }

    override fun draw(canvas: Canvas) = canvas.drawPath(path, paint)
    override fun setAlpha(alpha: Int) { paint.alpha = alpha }
    override fun setColorFilter(cf: ColorFilter?) { paint.colorFilter = cf }
    @Deprecated("Deprecated in Java")
    override fun getOpacity(): Int = PixelFormat.TRANSLUCENT
}

/**
 * Фон под всем экраном: «код» — падающие символы (кадр раз в 80 мс, только
 * пока экран виден), «автор» — бледная сетка логотипов, повёрнутая против
 * часовой стрелки. Нажатия пропускает насквозь.
 */
class BackdropView @JvmOverloads constructor(ctx: Context, attrs: AttributeSet? = null) : View(ctx, attrs) {
    var mode: String = ""
        set(v) {
            field = v
            cols = IntArray(0)
            restart()
            invalidate()
        }

    private val glyphs = "ｱｲｳｴｵｶｷｸｹｺｻｼｽｾｿﾀﾁﾂﾃﾄﾅﾆﾇﾈﾉﾊﾋﾌﾍﾎﾏﾐﾑﾒﾓﾔﾕﾖﾗﾘﾙﾚﾛﾜﾝ0123456789"
    private val fs = 14f * resources.displayMetrics.density
    private val textPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        typeface = Typeface.MONOSPACE
        textSize = fs
    }
    private var cols = IntArray(0)
    private val trail = ArrayList<Triple<Float, Float, Char>>()
    private val ticker = Handler(Looper.getMainLooper())
    private val tick = object : Runnable {
        override fun run() {
            if (mode != "code" || !isShown) return
            step()
            invalidate()
            ticker.postDelayed(this, 80)
        }
    }
    private val logo = ContextCompat.getDrawable(ctx, R.drawable.ic_logo)?.mutate()

    init {
        isClickable = false
        isFocusable = false
    }

    private fun restart() {
        ticker.removeCallbacks(tick)
        trail.clear()
        if (mode == "code") ticker.post(tick)
    }

    override fun onAttachedToWindow() {
        super.onAttachedToWindow()
        restart()
    }

    override fun onDetachedFromWindow() {
        ticker.removeCallbacks(tick)
        super.onDetachedFromWindow()
    }

    override fun onVisibilityChanged(changedView: View, visibility: Int) {
        super.onVisibilityChanged(changedView, visibility)
        if (visibility == VISIBLE) restart() else ticker.removeCallbacks(tick)
    }

    // Символы хранятся последние ~20 кадров на колонку и гаснут по возрасту —
    // без растрового буфера, который пришлось бы держать размером с экран.
    private fun step() {
        val n = (width / fs).toInt() + 1
        if (cols.size != n) cols = IntArray(n) { Random.nextInt((height / fs).toInt().coerceAtLeast(1)) }
        for (i in cols.indices) {
            trail.add(Triple(i * fs, cols[i] * fs, glyphs[Random.nextInt(glyphs.length)]))
            cols[i] = if (cols[i] * fs > height && Random.nextFloat() > .975f) 0 else cols[i] + 1
        }
        val keep = cols.size * 20
        if (trail.size > keep) trail.subList(0, trail.size - keep).clear()
    }

    override fun onDraw(canvas: Canvas) {
        when (mode) {
            "code" -> {
                val base = context.tc(R.color.ok)
                val total = trail.size.coerceAtLeast(1)
                trail.forEachIndexed { idx, (x, y, ch) ->
                    val age = idx.toFloat() / total
                    textPaint.color = base
                    textPaint.alpha = (age * 0.18f * 255).toInt()
                    canvas.drawText(ch.toString(), x, y, textPaint)
                }
            }
            "author" -> {
                val d = logo ?: return
                d.setTint(context.tc(R.color.accent))
                d.alpha = (0.06f * 255).toInt()
                val dp = resources.displayMetrics.density
                val w = (64 * dp).toInt(); val h = (53 * dp).toInt()
                val stepX = 108 * dp; val stepY = 96 * dp
                canvas.save()
                canvas.rotate(-35f, width / 2f, height / 2f)
                val span = (width + height).toFloat()
                var y = -span
                while (y < span * 1.5f) {
                    var x = -span
                    while (x < span * 1.5f) {
                        d.setBounds(x.toInt(), y.toInt(), x.toInt() + w, y.toInt() + h)
                        d.draw(canvas)
                        x += stepX
                    }
                    y += stepY
                }
                canvas.restore()
            }
        }
    }
}
