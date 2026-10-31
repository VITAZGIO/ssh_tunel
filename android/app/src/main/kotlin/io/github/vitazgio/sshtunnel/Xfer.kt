package io.github.vitazgio.sshtunnel

import android.app.Activity
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.net.Uri
import android.widget.LinearLayout

/**
 * Перенос настроек и списков: через буфер обмена или файлом — как выбрано
 * маленьким переключателем «буфер / файл» (Settings.xferMode). Выбор один на
 * всё приложение: кому удобнее файлами, тот не переключает каждый раз.
 */
object Xfer {

    /** Переключатель «буфер / файл»: подсветка выбранного и смена по нажатию. */
    fun bindModeSeg(box: LinearLayout, settings: Settings, onChange: () -> Unit = {}) {
        fun render() {
            for (k in 0 until box.childCount) {
                val c = box.getChildAt(k)
                if (c.tag?.toString() == settings.xferMode) c.setBackgroundResource(R.drawable.bg_seg_on)
                else c.setBackgroundResource(0)
            }
        }
        for (k in 0 until box.childCount) {
            val c = box.getChildAt(k)
            c.setOnClickListener {
                settings.xferMode = c.tag?.toString() ?: "clip"
                render()
                onChange()
            }
        }
        render()
    }

    fun toFile(settings: Settings): Boolean = settings.xferMode == "file"

    fun copy(context: Context, label: String, text: String) {
        val cm = context.getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager
        cm?.setPrimaryClip(ClipData.newPlainText(label, text))
    }

    fun paste(context: Context): String {
        val cm = context.getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager
        return cm?.primaryClip?.takeIf { it.itemCount > 0 }
            ?.getItemAt(0)?.coerceToText(context)?.toString().orEmpty()
    }

    fun write(activity: Activity, uri: Uri, text: String): Boolean = try {
        activity.contentResolver.openOutputStream(uri, "wt")?.use { it.write(text.toByteArray()) } != null
    } catch (e: Exception) {
        false
    }

    fun read(activity: Activity, uri: Uri): String = try {
        activity.contentResolver.openInputStream(uri)?.use { it.readBytes().toString(Charsets.UTF_8) }.orEmpty()
    } catch (e: Exception) {
        ""
    }
}
