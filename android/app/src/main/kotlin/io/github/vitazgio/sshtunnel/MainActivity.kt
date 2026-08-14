package io.github.vitazgio.sshtunnel

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.view.View
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.ImageView
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast
import android.widget.ViewFlipper
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import com.google.android.material.bottomnavigation.BottomNavigationView
import org.json.JSONObject

/**
 * Три экрана: главный, журнал и настройки — как в окне на компьютере.
 *
 * Экран ничем не управляет напрямую: он просит службу включиться или
 * выключиться и показывает то, что она сообщает. Поэтому туннель продолжает
 * работать, даже когда приложение закрыто.
 */
class MainActivity : AppCompatActivity() {

    private lateinit var settings: Settings

    private lateinit var flipper: ViewFlipper
    private lateinit var power: View
    private lateinit var powerIcon: ImageView
    private lateinit var powerCap: TextView
    private lateinit var stateView: TextView
    private lateinit var detailView: TextView
    private lateinit var rejectedView: TextView
    private lateinit var logView: TextView
    private lateinit var logScroll: ScrollView
    private lateinit var keyStateView: TextView

    private lateinit var tileDown: TextView
    private lateinit var tileUp: TextView
    private lateinit var tileConns: TextView
    private lateinit var tileLinks: TextView

    private lateinit var hostEdit: EditText
    private lateinit var portEdit: EditText
    private lateinit var poolEdit: EditText
    private lateinit var userEdit: EditText
    private lateinit var keyEdit: EditText
    private lateinit var directEdit: EditText
    private lateinit var localCheck: CheckBox

    private val vpnPermission = registerForActivityResult(
        androidx.activity.result.contract.ActivityResultContracts.StartActivityForResult()
    ) { result ->
        if (result.resultCode == RESULT_OK) start()
    }

    private val notificationPermission = registerForActivityResult(
        androidx.activity.result.contract.ActivityResultContracts.RequestPermission()
    ) { /* отказ не мешает работе, просто не будет уведомления */ }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        settings = Settings(this)

        flipper = findViewById(R.id.flipper)
        power = findViewById(R.id.power)
        powerIcon = findViewById(R.id.powerIcon)
        powerCap = findViewById(R.id.powerCap)
        stateView = findViewById(R.id.state)
        detailView = findViewById(R.id.detail)
        rejectedView = findViewById(R.id.rejected)
        logView = findViewById(R.id.log)
        logScroll = findViewById(R.id.logScroll)
        keyStateView = findViewById(R.id.keyState)

        tileDown = findViewById(R.id.tileDown)
        tileUp = findViewById(R.id.tileUp)
        tileConns = findViewById(R.id.tileConns)
        tileLinks = findViewById(R.id.tileLinks)

        hostEdit = findViewById(R.id.host)
        portEdit = findViewById(R.id.port)
        poolEdit = findViewById(R.id.pool)
        userEdit = findViewById(R.id.user)
        keyEdit = findViewById(R.id.key)
        directEdit = findViewById(R.id.direct)
        localCheck = findViewById(R.id.localViaTunnel)

        loadSettings()

        power.setOnClickListener { onToggle() }
        findViewById<Button>(R.id.save).setOnClickListener { saveSettings() }
        findViewById<Button>(R.id.apps).setOnClickListener {
            startActivity(Intent(this, AppsActivity::class.java))
        }

        findViewById<BottomNavigationView>(R.id.nav).setOnItemSelectedListener { item ->
            flipper.displayedChild = when (item.itemId) {
                R.id.navLog -> 1
                R.id.navSettings -> 2
                else -> 0
            }
            true
        }

        askNotificationPermission()
    }

    override fun onResume() {
        super.onResume()
        TunnelService.onUpdate = { runOnUiThread { refresh() } }
        refresh()
    }

    override fun onPause() {
        TunnelService.onUpdate = null
        super.onPause()
    }

    private fun loadSettings() {
        hostEdit.setText(settings.host)
        portEdit.setText(settings.sshPort.toString())
        poolEdit.setText(settings.poolSize.toString())
        userEdit.setText(settings.user)
        directEdit.setText(settings.directHosts)
        localCheck.isChecked = settings.localViaTunnel
    }

    private fun saveSettings() {
        settings.host = hostEdit.text.toString()
        settings.sshPort = portEdit.text.toString().toIntOrNull() ?: 22
        settings.poolSize = poolEdit.text.toString().toIntOrNull() ?: 4
        settings.user = userEdit.text.toString()
        settings.directHosts = directEdit.text.toString()
        settings.localViaTunnel = localCheck.isChecked

        // Ключ вводится один раз: после сохранения поле очищается, чтобы он не
        // лежал на экране у всех на виду.
        val key = keyEdit.text.toString()
        if (key.isNotBlank()) {
            settings.saveKey(key)
            keyEdit.setText("")
        }
        refresh()
        Toast.makeText(this, R.string.saved, Toast.LENGTH_SHORT).show()
    }

    private fun onToggle() {
        if (TunnelService.state != "stopped") {
            startService(Intent(this, TunnelService::class.java).setAction(TunnelService.ACTION_STOP))
            return
        }
        if (!settings.ready()) {
            Toast.makeText(this, R.string.need_settings, Toast.LENGTH_LONG).show()
            flipper.displayedChild = 2
            return
        }
        // Разрешение на VPN-подключение спрашивает сама система; без её
        // подтверждения служба не запустится.
        val intent = VpnService.prepare(this)
        if (intent != null) vpnPermission.launch(intent) else start()
    }

    private fun start() {
        startService(Intent(this, TunnelService::class.java).setAction(TunnelService.ACTION_START))
    }

    private fun askNotificationPermission() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        val granted = checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) ==
            PackageManager.PERMISSION_GRANTED
        if (!granted) notificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
    }

    private fun refresh() {
        val state = TunnelService.state

        stateView.setText(
            when (state) {
                "connected" -> R.string.state_connected
                "connecting" -> R.string.state_connecting
                "reconnecting" -> R.string.state_reconnecting
                "error" -> R.string.state_error
                else -> R.string.state_stopped
            }
        )
        // Цвет круга — то же соглашение, что на компьютере: зелёный работает,
        // жёлтый в процессе, красный сломалось.
        val bg: Int
        val tint: Int
        when (state) {
            "connected" -> { bg = R.drawable.bg_power_on; tint = R.color.ok }
            "connecting", "reconnecting" -> { bg = R.drawable.bg_power_busy; tint = R.color.warn }
            "error" -> { bg = R.drawable.bg_power_bad; tint = R.color.err }
            else -> { bg = R.drawable.bg_power_off; tint = R.color.dim }
        }
        power.setBackgroundResource(bg)
        val color = ContextCompat.getColor(this, tint)
        powerIcon.setColorFilter(color)
        powerCap.setTextColor(color)
        powerCap.setText(if (state == "stopped") R.string.start else R.string.stop)

        detailView.text = TunnelService.detail
        keyStateView.setText(if (settings.hasKey) R.string.key_saved else R.string.key_missing)

        showStats()

        val lines = synchronized(TunnelService.log) { TunnelService.log.toList() }
        logView.text = lines.joinToString("\n")
        logScroll.post { logScroll.fullScroll(View.FOCUS_DOWN) }
    }

    private fun showStats() {
        val o = try {
            JSONObject(TunnelService.statsJson)
        } catch (e: Exception) {
            JSONObject()
        }
        tileDown.text = size(o.optLong("bytesDown"))
        tileUp.text = size(o.optLong("bytesUp"))
        tileConns.text = o.optLong("total").toString()
        tileLinks.text = "${o.optInt("healthy")} / ${o.optInt("links")}"

        rejectedView.text = if (TunnelService.state == "stopped") {
            ""
        } else {
            "имён разрешено ${o.optInt("dnsAsked")} · отклонено: " +
                "UDP ${o.optInt("udpDropped")}, IPv6 ${o.optInt("v6Blocked")}"
        }
    }

    private fun size(bytes: Long): String = when {
        bytes >= 1024L * 1024 * 1024 -> String.format("%.1f ГБ", bytes / 1024.0 / 1024 / 1024)
        bytes >= 1024L * 1024 -> String.format("%.1f МБ", bytes / 1024.0 / 1024)
        bytes >= 1024L -> String.format("%.0f КБ", bytes / 1024.0)
        else -> "$bytes Б"
    }
}
