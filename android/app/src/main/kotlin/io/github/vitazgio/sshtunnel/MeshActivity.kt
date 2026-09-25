package io.github.vitazgio.sshtunnel

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.graphics.drawable.GradientDrawable
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.Gravity
import android.view.View
import android.widget.CheckBox
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import mobile.Mobile
import org.json.JSONObject

/**
 * Сеть устройств (см. docs/MESH.md): телефон, компьютер и домашний сервер
 * видят друг друга по именам «имя.mesh» через свой сервер. Настройки — у
 * активного сервера; применяются при следующем подключении.
 */
class MeshActivity : AppCompatActivity() {

    private lateinit var settings: Settings
    private lateinit var enabledCheck: CheckBox
    private lateinit var nameEdit: EditText
    private lateinit var keyEdit: EditText
    private lateinit var incomingCheck: CheckBox
    private lateinit var stateView: TextView
    private lateinit var peersBox: LinearLayout

    private val handler = Handler(Looper.getMainLooper())
    private val poll = object : Runnable {
        override fun run() {
            renderStatus()
            handler.postDelayed(this, 3000)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_mesh)
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

        findViewById<View>(R.id.backFromMesh).setOnClickListener { save(); finish() }

        enabledCheck = findViewById(R.id.meshEnabled)
        nameEdit = findViewById(R.id.meshName)
        keyEdit = findViewById(R.id.meshKey)
        incomingCheck = findViewById(R.id.meshIncoming)
        stateView = findViewById(R.id.meshState)
        peersBox = findViewById(R.id.meshPeers)

        val p = settings.active()
        findViewById<TextView>(R.id.meshServer).text = getString(R.string.mesh_for_server, p.name)
        enabledCheck.isChecked = p.meshEnabled
        nameEdit.setText(p.meshName)
        nameEdit.hint = android.os.Build.MODEL ?: ""
        keyEdit.setText(p.meshKey)
        incomingCheck.isChecked = p.meshIncoming

        // Включили без ключа — это первое устройство новой сети.
        enabledCheck.setOnCheckedChangeListener { _, checked ->
            if (checked && keyEdit.text.isBlank()) keyEdit.setText(Mobile.newMeshKey())
        }
        findViewById<View>(R.id.meshNewKey).setOnClickListener { askNewKey() }
        findViewById<View>(R.id.meshCopyKey).setOnClickListener {
            val cm = getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager
            cm?.setPrimaryClip(ClipData.newPlainText(getString(R.string.mesh_key), keyEdit.text.toString().trim()))
            Toast.makeText(this, R.string.mesh_key_copied, Toast.LENGTH_SHORT).show()
        }
        findViewById<View>(R.id.meshPasteKey).setOnClickListener {
            val cm = getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager
            val text = cm?.primaryClip?.takeIf { it.itemCount > 0 }
                ?.getItemAt(0)?.coerceToText(this)?.toString().orEmpty().trim()
            if (text.isBlank()) {
                Toast.makeText(this, R.string.paste_failed, Toast.LENGTH_SHORT).show()
            } else {
                keyEdit.setText(text)
                enabledCheck.isChecked = true
            }
        }
    }

    override fun onResume() {
        super.onResume()
        handler.post(poll)
    }

    override fun onPause() {
        handler.removeCallbacks(poll)
        save()
        super.onPause()
    }

    private fun askNewKey() {
        if (keyEdit.text.isBlank()) {
            keyEdit.setText(Mobile.newMeshKey())
            enabledCheck.isChecked = true
            return
        }
        AlertDialog.Builder(this)
            .setMessage(R.string.mesh_new_confirm)
            .setNegativeButton(R.string.mesh_cancel, null)
            .setPositiveButton(R.string.mesh_new) { _, _ ->
                keyEdit.setText(Mobile.newMeshKey())
                enabledCheck.isChecked = true
            }
            .show()
    }

    private fun save() {
        val p = settings.active()
        p.meshEnabled = enabledCheck.isChecked && keyEdit.text.isNotBlank()
        p.meshKey = keyEdit.text.toString().trim()
        p.meshName = nameEdit.text.toString().trim()
        p.meshIncoming = incomingCheck.isChecked
        settings.saveProfile(p)
    }

    private fun renderStatus() {
        val o = try { JSONObject(TunnelService.meshStatus()) } catch (e: Exception) { JSONObject() }
        val state = o.optString("state", "stopped")
        val self = o.optJSONObject("self")
        var text = when (state) {
            "online" -> getString(R.string.mesh_state_online)
            "connecting" -> getString(R.string.mesh_state_connecting)
            "error" -> getString(R.string.mesh_state_error)
            // Галочку включили, а туннель поднят ещё без сети устройств —
            // заработает после переподключения.
            "off" -> getString(if (enabledCheck.isChecked) R.string.mesh_state_pending else R.string.mesh_off)
            else -> getString(R.string.mesh_state_stopped)
        }
        if (state == "online" && self != null) {
            text += " — " + self.optString("host") + ".mesh (" + self.optString("ip") + ")"
        }
        o.optString("error").takeIf { it.isNotBlank() }?.let { text += ": $it" }
        o.optJSONObject("nat")?.let { text += "\n" + getString(R.string.mesh_nat) + ": " + natText(it) }
        stateView.text = text

        peersBox.removeAllViews()
        val peers = o.optJSONArray("peers") ?: return
        for (i in 0 until peers.length()) {
            val peer = peers.getJSONObject(i)
            peersBox.addView(peerRow(peer))
        }
    }

    /** Итог проверки NAT одной строкой (см. internal/mesh/natprobe.go). */
    private fun natText(n: JSONObject): String {
        if (!n.optBoolean("udp")) return getString(R.string.nat_udp_blocked)
        var s = when (n.optString("mapping")) {
            "independent" -> getString(R.string.nat_easy)
            "dependent" -> getString(R.string.nat_hard)
            else -> "?"
        }
        if (n.optString("filtering") == "open") s += ", " + getString(R.string.nat_open)
        if (n.optBoolean("ipv6")) s += ", IPv6"
        n.optString("publicIp").takeIf { it.isNotBlank() }?.let { s += " · $it" }
        return s
    }

    private fun peerRow(peer: JSONObject): View {
        val host = peer.optString("host") + ".mesh"
        val row = LinearLayout(this)
        row.orientation = LinearLayout.HORIZONTAL
        row.gravity = Gravity.CENTER_VERTICAL
        row.background = ContextCompat.getDrawable(this, R.drawable.bg_card)
        row.setPadding(dp(12), dp(9), dp(12), dp(9))
        val lp = LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT)
        lp.bottomMargin = dp(6)
        row.layoutParams = lp

        val dot = View(this)
        val d = GradientDrawable()
        d.shape = GradientDrawable.OVAL
        d.setColor(ContextCompat.getColor(this, if (peer.optBoolean("online")) R.color.ok else R.color.dim))
        dot.background = d
        row.addView(dot, LinearLayout.LayoutParams(dp(8), dp(8)).also { it.marginEnd = dp(10) })

        val texts = LinearLayout(this)
        texts.orientation = LinearLayout.VERTICAL
        val h = TextView(this)
        h.text = if (peer.optBoolean("self")) host + " " + getString(R.string.mesh_this) else host
        h.typeface = android.graphics.Typeface.MONOSPACE
        h.textSize = 13f
        h.setTextColor(ContextCompat.getColor(this, R.color.text))
        val n = TextView(this)
        n.text = peer.optString("name")
        n.textSize = 12f
        n.setTextColor(ContextCompat.getColor(this, R.color.dim))
        texts.addView(h)
        texts.addView(n)
        row.addView(texts, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))

        val ip = TextView(this)
        ip.text = peer.optString("ip")
        ip.typeface = android.graphics.Typeface.MONOSPACE
        ip.textSize = 12f
        ip.setTextColor(ContextCompat.getColor(this, R.color.dim))
        row.addView(ip)

        row.setOnClickListener {
            val cm = getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager
            cm?.setPrimaryClip(ClipData.newPlainText(host, host))
            Toast.makeText(this, getString(R.string.mesh_name_copied, host), Toast.LENGTH_SHORT).show()
        }
        return row
    }

    private fun dp(v: Int): Int = (v * resources.displayMetrics.density).toInt()
}
