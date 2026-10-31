package io.github.vitazgio.sshtunnel

import android.os.Bundle
import android.view.View
import android.view.ViewGroup
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.PopupWindow
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import org.json.JSONArray
import org.json.JSONObject

/**
 * Исключения доменов: «всегда напрямую» и «всегда в туннель» — два списка,
 * общие для всех серверов (как выбор приложений). Кнопки сверху сдвигают
 * ленту вбок, внизу — перенос обоих списков буфером или файлом.
 */
class DomainsActivity : AppCompatActivity() {

    private lateinit var settings: Settings
    private lateinit var directEdit: EditText
    private lateinit var tunnelEdit: EditText
    private lateinit var track: View
    private lateinit var tabs: LinearLayout
    private lateinit var note: TextView
    private var onTunnel = false

    private val saveFile = registerForActivityResult(
        ActivityResultContracts.CreateDocument("application/json")
    ) { uri ->
        if (uri != null) {
            note.setText(if (Xfer.write(this, uri, exportText())) R.string.saved_to_file else R.string.file_failed)
        }
    }

    private val openFile = registerForActivityResult(ActivityResultContracts.OpenDocument()) { uri ->
        if (uri != null) importText(Xfer.read(this, uri))
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        Look.apply(this)
        setContentView(R.layout.activity_domains)
        Look.backdrop(this)
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

        directEdit = findViewById(R.id.directEdit)
        tunnelEdit = findViewById(R.id.tunnelEdit)
        track = findViewById(R.id.domTrack)
        tabs = findViewById(R.id.domTabs)
        note = findViewById(R.id.domNote)

        directEdit.setText(settings.directHosts)
        tunnelEdit.setText(settings.tunnelHosts)

        // Каждая панель — ровно в ширину видимой области: лента вдвое шире.
        val viewport = findViewById<View>(R.id.domViewport)
        viewport.addOnLayoutChangeListener { _, l, _, r, _, _, _, _, _ ->
            val w = r - l
            for (id in listOf(R.id.paneDirect, R.id.paneTunnel)) {
                val pane = findViewById<View>(id)
                if (w > 0 && pane.layoutParams.width != w) {
                    pane.layoutParams = pane.layoutParams.also { it.width = w }
                    track.translationX = if (onTunnel) -w.toFloat() else 0f
                }
            }
        }
        for (k in 0 until tabs.childCount) {
            val c = tabs.getChildAt(k)
            c.setOnClickListener { setTab(c.tag == "tunnel", true) }
        }
        setTab(false, false)

        findViewById<View>(R.id.directInfo).setOnClickListener { showTip(it, getString(R.string.direct_note)) }
        findViewById<View>(R.id.tunnelInfo).setOnClickListener { showTip(it, getString(R.string.tunnel_note)) }
        Xfer.bindModeSeg(findViewById(R.id.xferModeSeg), settings)
        findViewById<View>(R.id.domExportBtn).setOnClickListener { onExport() }
        findViewById<View>(R.id.domImportBtn).setOnClickListener { onImport() }
        findViewById<View>(R.id.backFromDomains).setOnClickListener { save(); finish() }
        findViewById<View>(R.id.saveDomains).setOnClickListener {
            save()
            Toast.makeText(this, R.string.saved, Toast.LENGTH_SHORT).show()
            finish()
        }
    }

    override fun onPause() {
        super.onPause()
        save()
    }

    private fun setTab(tunnel: Boolean, animate: Boolean) {
        onTunnel = tunnel
        for (k in 0 until tabs.childCount) {
            val c = tabs.getChildAt(k)
            c.setBackgroundResource(if ((c.tag == "tunnel") == tunnel) R.drawable.bg_seg_on else 0)
        }
        val w = findViewById<View>(R.id.domViewport).width.toFloat()
        val x = if (tunnel) -w else 0f
        if (animate) track.animate().translationX(x).setDuration(300).start() else track.translationX = x
        // Фокус не остаётся в уехавшем за край поле.
        if (directEdit.hasFocus() || tunnelEdit.hasFocus()) (if (tunnel) tunnelEdit else directEdit).requestFocus()
    }

    /** Списки общие для всех серверов — как и на компьютере. */
    private fun save() {
        val p = settings.active()
        p.directHosts = directEdit.text.toString().trim()
        p.tunnelHosts = tunnelEdit.text.toString().trim()
        settings.saveProfile(p)
        settings.shareTraffic(p)
    }

    private fun entries(text: String): List<String> =
        text.split(Regex("[\\s,;]+")).map { it.trim() }.filter { it.isNotEmpty() }

    /** Тот же формат, что у компьютера: {"sshTunnelDomains":1,"direct":[…],"tunnel":[…]}. */
    private fun exportText(): String = JSONObject()
        .put("sshTunnelDomains", 1)
        .put("direct", JSONArray(entries(directEdit.text.toString())))
        .put("tunnel", JSONArray(entries(tunnelEdit.text.toString())))
        .toString(2)

    private fun onExport() {
        if (Xfer.toFile(settings)) {
            saveFile.launch("ssh_tunnel-domains.json")
            return
        }
        Xfer.copy(this, getString(R.string.domains_title), exportText())
        note.setText(R.string.copied)
    }

    private fun onImport() {
        if (Xfer.toFile(settings)) {
            openFile.launch(arrayOf("application/json", "text/plain", "*/*"))
            return
        }
        importText(Xfer.paste(this))
    }

    private fun importText(text: String) {
        if (text.isBlank()) {
            note.setText(R.string.paste_failed)
            return
        }
        val o = try { JSONObject(text.trim()) } catch (e: Exception) { null }
        when {
            o != null && o.has("sshTunnelDomains") -> {
                directEdit.setText(list(o.optJSONArray("direct")))
                tunnelEdit.setText(list(o.optJSONArray("tunnel")))
            }
            o == null && entries(text).isNotEmpty() ->
                // Просто список имён — в тот, что сейчас открыт.
                (if (onTunnel) tunnelEdit else directEdit).setText(entries(text).joinToString("\n"))
            else -> {
                note.setText(R.string.domains_bad)
                return
            }
        }
        save()
        note.setText(R.string.domains_imported)
    }

    private fun list(a: JSONArray?): String {
        if (a == null) return ""
        return (0 until a.length()).joinToString("\n") { a.optString(it) }
    }

    private fun showTip(anchor: View, text: String) {
        val tv = TextView(this)
        tv.text = text
        tv.setTextColor(tc(R.color.text))
        tv.textSize = 12.5f
        tv.setPadding(dp(12), dp(10), dp(12), dp(10))
        val popup = PopupWindow(tv, dp(260), ViewGroup.LayoutParams.WRAP_CONTENT, true)
        popup.setBackgroundDrawable(ContextCompat.getDrawable(this, R.drawable.bg_card))
        popup.elevation = 12f
        popup.showAsDropDown(anchor, 0, 4)
    }

    private fun dp(v: Int): Int = (v * resources.displayMetrics.density).toInt()
}
