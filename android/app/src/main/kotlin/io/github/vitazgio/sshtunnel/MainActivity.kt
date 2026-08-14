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
import org.json.JSONObject

/**
 * Экран приложения — повторяет окно на компьютере.
 *
 * Сам он ничем не управляет: просит службу включиться или выключиться и
 * показывает то, что она сообщает. Поэтому туннель продолжает работать, даже
 * когда приложение закрыто.
 */
class MainActivity : AppCompatActivity() {

    private companion object {
        const val MAIN = 0
        const val LOG = 1
        const val SETTINGS = 2
    }

    private lateinit var settings: Settings

    private lateinit var flipper: ViewFlipper
    private lateinit var power: View
    private lateinit var powerIcon: ImageView
    private lateinit var powerCap: TextView
    private lateinit var stateView: TextView
    private lateinit var detailView: TextView
    private lateinit var errorView: TextView
    private lateinit var logView: TextView
    private lateinit var logScroll: ScrollView
    private lateinit var keyStateView: TextView
    private lateinit var appsSummary: TextView
    private lateinit var speedButton: Button

    private lateinit var pingView: TextView
    private lateinit var rowSpeed: View
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
        errorView = findViewById(R.id.error)
        logView = findViewById(R.id.log)
        logScroll = findViewById(R.id.logScroll)
        keyStateView = findViewById(R.id.keyState)
        appsSummary = findViewById(R.id.appsSummary)
        speedButton = findViewById(R.id.speedtest)

        pingView = findViewById(R.id.ping)
        rowSpeed = findViewById(R.id.rowSpeed)
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
        speedButton.setOnClickListener { runSpeedTest() }
        findViewById<Button>(R.id.save).setOnClickListener { saveSettings() }
        findViewById<View>(R.id.apps).setOnClickListener {
            startActivity(Intent(this, AppsActivity::class.java))
        }

        // Шапка одна на все экраны: логотип всегда возвращает на главную,
        // значки всегда открывают своё. Так не надо помнить, где находишься.
        findViewById<View>(R.id.btnHome).setOnClickListener { flipper.displayedChild = MAIN }
        findViewById<View>(R.id.btnLog).setOnClickListener { flipper.displayedChild = LOG }
        findViewById<View>(R.id.btnSettings).setOnClickListener { flipper.displayedChild = SETTINGS }

        applyInsets()

        // Кнопка «назад» возвращает на главную, а не закрывает приложение:
        // выйти из настроек хочется чаще, чем выйти совсем.
        onBackPressedDispatcher.addCallback(this,
            object : androidx.activity.OnBackPressedCallback(true) {
                override fun handleOnBackPressed() {
                    if (flipper.displayedChild != MAIN) {
                        flipper.displayedChild = MAIN
                    } else {
                        isEnabled = false
                        onBackPressedDispatcher.onBackPressed()
                    }
                }
            })

        askNotificationPermission()
    }

    /**
     * Отступы под системные полосы.
     *
     * Начиная с Android 15 окно занимает экран целиком, вместе с полосой часов
     * и вырезом под камеру. Без этого шапка оказывается прямо под ними.
     * Значения берём у системы, а не подбираем на глаз: у каждого телефона
     * они свои.
     */
    private fun applyInsets() {
        val root = findViewById<View>(R.id.root)
        androidx.core.view.ViewCompat.setOnApplyWindowInsetsListener(root) { view, insets ->
            val bars = insets.getInsets(
                androidx.core.view.WindowInsetsCompat.Type.systemBars() or
                    androidx.core.view.WindowInsetsCompat.Type.displayCutout()
            )
            view.setPadding(bars.left, bars.top, bars.right, bars.bottom)
            insets
        }
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
            flipper.displayedChild = SETTINGS
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

    /** Тест скорости идёт секунд двадцать, поэтому в отдельном потоке. */
    private fun runSpeedTest() {
        if (TunnelService.state != "connected") {
            Toast.makeText(this, R.string.need_connected, Toast.LENGTH_SHORT).show()
            return
        }
        speedButton.isEnabled = false
        speedButton.setText(R.string.speedtest_running)
        Thread {
            val out = try {
                TunnelService.speedTest()
            } catch (e: Exception) {
                """{"error":"${e.message}"}"""
            }
            runOnUiThread {
                speedButton.isEnabled = true
                speedButton.setText(R.string.speedtest)
                showSpeed(out)
            }
        }.start()
    }

    /**
     * Показывает результат замера под состоянием и убирает его через десять
     * секунд: сразу после теста цифры интересны, а через минуту они уже
     * неправда — связь меняется.
     */
    private fun showSpeed(json: String) {
        val o = try {
            JSONObject(json)
        } catch (e: Exception) {
            JSONObject()
        }
        val err = o.optString("error")
        if (err.isNotBlank()) {
            Toast.makeText(this, err, Toast.LENGTH_LONG).show()
            return
        }
        tileDown.text = "%.1f".format(o.optDouble("downMbps", 0.0))
        tileUp.text = "%.1f".format(o.optDouble("upMbps", 0.0))
        rowSpeed.visibility = View.VISIBLE
        rowSpeed.removeCallbacks(hideSpeed)
        rowSpeed.postDelayed(hideSpeed, 10_000)
    }

    private val hideSpeed = Runnable { rowSpeed.visibility = View.GONE }

    /** Задержка до сервера. Цвет важнее числа: зелёный, жёлтый, красный. */
    private fun showPing(ms: Long) {
        if (ms <= 0) {
            pingView.text = ""
            return
        }
        pingView.text = "$ms мс"
        pingView.setTextColor(
            ContextCompat.getColor(
                this,
                when {
                    ms < 100 -> R.color.ok
                    ms < 250 -> R.color.warn
                    else -> R.color.err
                }
            )
        )
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

        if (state == "error" && TunnelService.detail.isNotBlank()) {
            errorView.text = TunnelService.detail
            errorView.visibility = View.VISIBLE
            detailView.text = ""
        } else {
            errorView.visibility = View.GONE
            detailView.text = TunnelService.detail
        }

        keyStateView.setText(if (settings.hasKey) R.string.key_saved else R.string.key_missing)
        appsSummary.text = appsSummaryText()

        showStats(state)

        val lines = synchronized(TunnelService.log) { TunnelService.log.toList() }
        logView.text = lines.joinToString("\n")
        logScroll.post { logScroll.fullScroll(View.FOCUS_DOWN) }
    }

    private fun appsSummaryText(): String = when (settings.filterMode) {
        "only" -> getString(R.string.mode_only) + ", " +
            getString(R.string.apps_chosen, settings.filterApps.size)
        "except" -> getString(R.string.mode_except) + ", " +
            getString(R.string.apps_chosen, settings.filterApps.size)
        else -> getString(R.string.mode_all)
    }

    private fun showStats(state: String) {
        val o = try {
            JSONObject(TunnelService.statsJson)
        } catch (e: Exception) {
            JSONObject()
        }
        val running = state != "stopped"
        tileConns.text = if (running) o.optLong("total").toString() else "—"
        tileLinks.text = if (running) "${o.optInt("healthy")} / ${o.optInt("links")}" else "—"

        showPing(if (state == "connected") o.optLong("pingMs") else 0L)
        if (!running) {
            rowSpeed.visibility = View.GONE
            rowSpeed.removeCallbacks(hideSpeed)
        }
    }
}
