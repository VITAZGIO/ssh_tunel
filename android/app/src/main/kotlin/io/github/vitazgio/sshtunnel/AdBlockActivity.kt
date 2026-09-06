package io.github.vitazgio.sshtunnel

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.os.Bundle
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.PopupWindow
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import mobile.Mobile
import org.json.JSONObject

/**
 * Блокировка рекламы — отдельным экраном, как выбор приложений: два списка,
 * которые применяются одновременно (см. ad_block_tab_block/ad_block_tab_allow
 * в strings.xml) — чёрный список (что блокировать) и белый список (что не
 * трогать, даже если совпало с чёрным). Раньше оба текстовых поля были
 * зажаты прямо в общих настройках — здесь у каждого простор и кнопки
 * вставки/копирования буфера обмена.
 */
class AdBlockActivity : AppCompatActivity() {

    private lateinit var settings: Settings

    private lateinit var enabledCheck: CheckBox
    private lateinit var tileBlocked: TextView
    private lateinit var tabBlockBtn: Button
    private lateinit var tabAllowBtn: Button
    private lateinit var tabLabel: TextView
    private lateinit var listEdit: EditText
    private lateinit var updateButton: Button
    private lateinit var statusView: TextView

    /** true — редактируется чёрный список (adBlockSources), false — белый (adBlockAllowlist). */
    private var onBlockTab = true

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_adblock)
        settings = Settings(this)

        androidx.core.view.ViewCompat.setOnApplyWindowInsetsListener(
            findViewById(R.id.root)
        ) { view, insets ->
            val bars = insets.getInsets(
                androidx.core.view.WindowInsetsCompat.Type.systemBars() or
                    androidx.core.view.WindowInsetsCompat.Type.displayCutout()
            )
            view.setPadding(bars.left + view.paddingLeft, bars.top, bars.right + view.paddingRight, bars.bottom)
            insets
        }

        findViewById<View>(R.id.backFromAdBlock).setOnClickListener { saveCurrentTab(); finish() }

        enabledCheck = findViewById(R.id.adBlockEnabled)
        tileBlocked = findViewById(R.id.tileBlocked)
        tabBlockBtn = findViewById(R.id.tabBlockBtn)
        tabAllowBtn = findViewById(R.id.tabAllowBtn)
        tabLabel = findViewById(R.id.tabLabel)
        listEdit = findViewById(R.id.listEdit)
        updateButton = findViewById(R.id.adBlockUpdate)
        statusView = findViewById(R.id.adBlockStatus)

        enabledCheck.isChecked = settings.adBlockEnabled
        enabledCheck.setOnCheckedChangeListener { _, checked -> settings.adBlockEnabled = checked }

        tileBlocked.text = getString(R.string.tile_blocked) + ": " + settings.adBlockTotal

        tabBlockBtn.setOnClickListener { switchTab(true) }
        tabAllowBtn.setOnClickListener { switchTab(false) }
        switchTab(true)

        findViewById<View>(R.id.tabInfo).setOnClickListener {
            showTip(it, getString(if (onBlockTab) R.string.ad_block_sources_note else R.string.ad_block_allowlist_hint))
        }
        findViewById<View>(R.id.adBlockUpdateInfo).setOnClickListener {
            showTip(it, getString(R.string.ad_block_note))
        }

        findViewById<View>(R.id.pasteListBtn).setOnClickListener { pasteList() }
        findViewById<View>(R.id.copyListBtn).setOnClickListener { copyList() }
        updateButton.setOnClickListener { updateBlockLists() }
    }

    override fun onPause() {
        super.onPause()
        saveCurrentTab()
    }

    private fun switchTab(block: Boolean) {
        saveCurrentTab()
        onBlockTab = block
        tabBlockBtn.setBackgroundResource(if (block) R.drawable.bg_button_primary else R.drawable.bg_button)
        tabAllowBtn.setBackgroundResource(if (block) R.drawable.bg_button else R.drawable.bg_button_primary)
        // Цвет фона один поверх другого мог быть недостаточно заметен —
        // жирный белый текст на активной вкладке убирает всякую путаницу,
        // какой список сейчас открыт.
        tabBlockBtn.setTextColor(ContextCompat.getColor(this, if (block) android.R.color.white else R.color.text))
        tabBlockBtn.setTypeface(null, if (block) android.graphics.Typeface.BOLD else android.graphics.Typeface.NORMAL)
        tabAllowBtn.setTextColor(ContextCompat.getColor(this, if (block) R.color.text else android.R.color.white))
        tabAllowBtn.setTypeface(null, if (block) android.graphics.Typeface.NORMAL else android.graphics.Typeface.BOLD)
        if (block) {
            tabLabel.setText(R.string.ad_block_sources)
            listEdit.hint = getString(R.string.ad_block_sources_hint)
            listEdit.setText(settings.adBlockSources)
        } else {
            tabLabel.setText(R.string.ad_block_allowlist)
            listEdit.hint = getString(R.string.ad_block_allowlist_hint)
            listEdit.setText(settings.adBlockAllowlist)
        }
    }

    private fun saveCurrentTab() {
        val text = listEdit.text.toString()
        if (onBlockTab) settings.adBlockSources = text else settings.adBlockAllowlist = text
    }

    private fun pasteList() {
        val cm = getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager
        val text = cm?.primaryClip?.takeIf { it.itemCount > 0 }
            ?.getItemAt(0)?.coerceToText(this)?.toString().orEmpty()
        if (text.isBlank()) {
            Toast.makeText(this, R.string.paste_failed, Toast.LENGTH_SHORT).show()
            return
        }
        listEdit.setText(text)
    }

    private fun copyList() {
        val cm = getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager
        cm?.setPrimaryClip(ClipData.newPlainText(getString(R.string.ad_block_title), listEdit.text.toString()))
        Toast.makeText(this, R.string.adblock_copied, Toast.LENGTH_SHORT).show()
    }

    /**
     * Загружает списки блокировки по нажатию кнопки — не сама по себе, не по
     * расписанию. Сеть небыстрая, поэтому в отдельном потоке; результат
     * (сколько имён загружено, или ошибка) сохраняется на телефон самим ядром
     * и тут же показывается человеку. Работает только для чёрного списка —
     * источники ссылок есть только у него, белый список — просто текст.
     */
    private fun updateBlockLists() {
        if (!onBlockTab) switchTab(true)
        val sources = listEdit.text.toString()
        if (sources.isBlank()) {
            statusView.text = ""
            Toast.makeText(this, R.string.ad_block_sources_hint, Toast.LENGTH_SHORT).show()
            return
        }
        settings.adBlockSources = sources
        updateButton.isEnabled = false
        statusView.setText(R.string.ad_block_updating)
        Thread {
            val result = try {
                Mobile.updateBlockLists(sources, settings.adBlockListFile.absolutePath)
            } catch (e: Exception) {
                """{"error":"${e.message}"}"""
            }
            runOnUiThread {
                updateButton.isEnabled = true
                val o = try { JSONObject(result) } catch (e: Exception) { JSONObject() }
                val err = o.optString("error")
                statusView.text = if (err.isNotBlank()) {
                    getString(R.string.ad_block_update_failed, err)
                } else {
                    getString(R.string.ad_block_updated, o.optInt("count"))
                }
            }
        }.start()
    }

    private fun showTip(anchor: View, text: String) {
        val tv = TextView(this)
        tv.text = text
        tv.setTextColor(ContextCompat.getColor(this, R.color.text))
        tv.textSize = 12.5f
        tv.setPadding(dp(12), dp(10), dp(12), dp(10))
        val popup = PopupWindow(tv, dp(240), ViewGroup.LayoutParams.WRAP_CONTENT, true)
        popup.setBackgroundDrawable(ContextCompat.getDrawable(this, R.drawable.bg_card))
        popup.elevation = 12f
        popup.showAsDropDown(anchor, 0, 4)
    }

    private fun dp(v: Int): Int = (v * resources.displayMetrics.density).toInt()
}
