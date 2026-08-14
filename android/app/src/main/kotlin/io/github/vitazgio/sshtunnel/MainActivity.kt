package io.github.vitazgio.sshtunnel

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity

/**
 * Главный экран: состояние, кнопка и настройки.
 *
 * Экран ничем не управляет напрямую — он только просит службу включиться или
 * выключиться и показывает то, что она сообщает. Так туннель продолжает
 * работать, даже когда экран закрыт.
 */
class MainActivity : AppCompatActivity() {

    private lateinit var settings: Settings

    private lateinit var stateView: TextView
    private lateinit var detailView: TextView
    private lateinit var logView: TextView
    private lateinit var keyStateView: TextView
    private lateinit var toggle: Button

    private lateinit var hostEdit: EditText
    private lateinit var portEdit: EditText
    private lateinit var userEdit: EditText
    private lateinit var keyEdit: EditText
    private lateinit var directEdit: EditText
    private lateinit var localCheck: CheckBox

    /** Запрос системы «разрешить создание VPN-подключения». */
    private val vpnPermission = registerForActivityResult(
        androidx.activity.result.contract.ActivityResultContracts.StartActivityForResult()
    ) { result ->
        if (result.resultCode == RESULT_OK) {
            startService(Intent(this, TunnelService::class.java).setAction(TunnelService.ACTION_START))
        }
    }

    private val notificationPermission = registerForActivityResult(
        androidx.activity.result.contract.ActivityResultContracts.RequestPermission()
    ) { /* отказ не мешает работе, просто не будет уведомления */ }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        settings = Settings(this)

        stateView = findViewById(R.id.state)
        detailView = findViewById(R.id.detail)
        logView = findViewById(R.id.log)
        keyStateView = findViewById(R.id.keyState)
        toggle = findViewById(R.id.toggle)

        hostEdit = findViewById(R.id.host)
        portEdit = findViewById(R.id.port)
        userEdit = findViewById(R.id.user)
        keyEdit = findViewById(R.id.key)
        directEdit = findViewById(R.id.direct)
        localCheck = findViewById(R.id.localViaTunnel)

        loadSettings()

        findViewById<Button>(R.id.save).setOnClickListener { saveSettings() }
        findViewById<Button>(R.id.apps).setOnClickListener {
            startActivity(Intent(this, AppsActivity::class.java))
        }
        toggle.setOnClickListener { onToggle() }

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
        userEdit.setText(settings.user)
        directEdit.setText(settings.directHosts)
        localCheck.isChecked = settings.localViaTunnel
    }

    private fun saveSettings() {
        settings.host = hostEdit.text.toString()
        settings.sshPort = portEdit.text.toString().toIntOrNull() ?: 22
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
            return
        }
        // Система сама спрашивает у человека разрешение на VPN-подключение;
        // без её подтверждения служба не запустится.
        val intent = VpnService.prepare(this)
        if (intent != null) {
            vpnPermission.launch(intent)
        } else {
            startService(Intent(this, TunnelService::class.java).setAction(TunnelService.ACTION_START))
        }
    }

    private fun askNotificationPermission() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        val granted = checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) ==
            PackageManager.PERMISSION_GRANTED
        if (!granted) {
            notificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
    }

    private fun refresh() {
        stateView.setText(
            when (TunnelService.state) {
                "connected" -> R.string.state_connected
                "connecting" -> R.string.state_connecting
                "error" -> R.string.state_error
                else -> R.string.state_stopped
            }
        )
        detailView.text = TunnelService.detail
        toggle.setText(if (TunnelService.state == "stopped") R.string.start else R.string.stop)
        keyStateView.setText(if (settings.hasKey) R.string.key_saved else R.string.key_missing)

        val lines = synchronized(TunnelService.log) { TunnelService.log.toList() }
        logView.text = lines.takeLast(40).joinToString("\n")
    }
}
