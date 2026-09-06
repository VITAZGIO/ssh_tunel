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
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast
import android.widget.ViewFlipper
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import mobile.Mobile
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
        const val SELFCHECK = 1
        const val LOG = 2
        const val SETTINGS = 3
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
    private lateinit var tileBlocked: TextView

    private lateinit var hostEdit: EditText
    private lateinit var portEdit: EditText
    private lateinit var poolEdit: EditText
    private lateinit var userEdit: EditText
    private lateinit var keyEdit: EditText
    private lateinit var directEdit: EditText
    private lateinit var localCheck: CheckBox

    private lateinit var adBlockEnabledCheck: CheckBox
    private lateinit var adBlockSourcesEdit: EditText
    private lateinit var adBlockAllowlistEdit: EditText
    private lateinit var adBlockUpdateButton: Button
    private lateinit var adBlockStatusView: TextView

    private lateinit var selfCheckSteps: LinearLayout
    private lateinit var selfCheckStartButton: Button

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
        tileBlocked = findViewById(R.id.tileBlocked)

        hostEdit = findViewById(R.id.host)
        portEdit = findViewById(R.id.port)
        poolEdit = findViewById(R.id.pool)
        userEdit = findViewById(R.id.user)
        keyEdit = findViewById(R.id.key)
        directEdit = findViewById(R.id.direct)
        localCheck = findViewById(R.id.localViaTunnel)

        adBlockEnabledCheck = findViewById(R.id.adBlockEnabled)
        adBlockSourcesEdit = findViewById(R.id.adBlockSources)
        adBlockAllowlistEdit = findViewById(R.id.adBlockAllowlist)
        adBlockUpdateButton = findViewById(R.id.adBlockUpdate)
        adBlockStatusView = findViewById(R.id.adBlockStatus)

        selfCheckSteps = findViewById(R.id.selfCheckSteps)
        selfCheckStartButton = findViewById(R.id.selfCheckStart)
        renderSelfCheckSteps(null)

        loadSettings()

        power.setOnClickListener { onToggle() }
        speedButton.setOnClickListener { runSpeedTest() }
        findViewById<Button>(R.id.save).setOnClickListener { saveSettings() }
        adBlockUpdateButton.setOnClickListener { updateBlockLists() }
        selfCheckStartButton.setOnClickListener { runSelfCheck() }
        findViewById<View>(R.id.toSelfCheck).setOnClickListener {
            flipper.displayedChild = SELFCHECK
            runSelfCheck()
        }
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

        adBlockEnabledCheck.isChecked = settings.adBlockEnabled
        adBlockSourcesEdit.setText(settings.adBlockSources)
        adBlockAllowlistEdit.setText(settings.adBlockAllowlist)
    }

    private fun saveSettings() {
        settings.host = hostEdit.text.toString()
        settings.sshPort = portEdit.text.toString().toIntOrNull() ?: 22
        settings.poolSize = poolEdit.text.toString().toIntOrNull() ?: 4
        settings.user = userEdit.text.toString()
        settings.directHosts = directEdit.text.toString()
        settings.localViaTunnel = localCheck.isChecked

        settings.adBlockEnabled = adBlockEnabledCheck.isChecked
        settings.adBlockSources = adBlockSourcesEdit.text.toString()
        settings.adBlockAllowlist = adBlockAllowlistEdit.text.toString()

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

    // INVISIBLE, а не GONE: место под плитками остаётся занятым, и экран не
    // дёргается при каждом появлении результата.
    private val hideSpeed = Runnable { rowSpeed.visibility = View.INVISIBLE }

    /**
     * Загружает списки блокировки по нажатию кнопки — не сама по себе, не по
     * расписанию. Сеть небыстрая, поэтому в отдельном потоке; результат
     * (сколько имён загружено, или ошибка) сохраняется на телефон самим ядром
     * и тут же показывается человеку.
     */
    private fun updateBlockLists() {
        val sources = adBlockSourcesEdit.text.toString()
        if (sources.isBlank()) {
            adBlockStatusView.text = ""
            Toast.makeText(this, R.string.ad_block_sources_hint, Toast.LENGTH_SHORT).show()
            return
        }
        settings.adBlockSources = sources
        adBlockUpdateButton.isEnabled = false
        adBlockStatusView.setText(R.string.ad_block_updating)
        Thread {
            val result = try {
                Mobile.updateBlockLists(sources, settings.adBlockListFile.absolutePath)
            } catch (e: Exception) {
                """{"error":"${e.message}"}"""
            }
            runOnUiThread {
                adBlockUpdateButton.isEnabled = true
                val o = try { JSONObject(result) } catch (e: Exception) { JSONObject() }
                val err = o.optString("error")
                adBlockStatusView.text = if (err.isNotBlank()) {
                    getString(R.string.ad_block_update_failed, err)
                } else {
                    getString(R.string.ad_block_updated, o.optInt("count"))
                }
            }
        }.start()
    }

    private val SELFCHECK_STEP_NAMES = listOf("dns", "port", "key", "forward", "dns_tunnel", "sites", "external_ip")

    private fun selfCheckStepLabelRes(name: String): Int = when (name) {
        "dns" -> R.string.selfcheck_step_dns
        "port" -> R.string.selfcheck_step_port
        "key" -> R.string.selfcheck_step_key
        "forward" -> R.string.selfcheck_step_forward
        "dns_tunnel" -> R.string.selfcheck_step_dns_tunnel
        "sites" -> R.string.selfcheck_step_sites
        else -> R.string.selfcheck_step_external_ip
    }

    /**
     * Текст причины по коду шага. Именование ресурса ("sc_" + шаг + "_" + код)
     * зеркалит то же самое в JS-словаре на компьютере — так у обеих сторон
     * общая логика без необходимости заводить общий Go-код специально ради
     * текста. Ресурса нет (обычно для "успешных" кодов вроде "resolved") —
     * значит вся нужная информация уже в detail, его и показываем как есть.
     */
    private fun selfCheckCodeText(name: String, code: String, detail: String): String {
        val resId = resources.getIdentifier("sc_${name}_$code", "string", packageName)
        if (resId != 0) {
            return try { getString(resId, detail) } catch (e: Exception) { getString(resId) }
        }
        return detail.ifBlank { code }
    }

    /** steps=null — все семь «в ожидании», как до первого запуска. */
    private fun renderSelfCheckSteps(steps: List<JSONObject>?) {
        selfCheckSteps.removeAllViews()
        val byName = HashMap<String, JSONObject>()
        steps?.forEach { byName[it.optString("name")] = it }

        for (name in SELFCHECK_STEP_NAMES) {
            val s = byName[name]
            val mark: String
            val colorRes: Int
            when {
                s == null -> { mark = "…"; colorRes = R.color.dim }
                s.optBoolean("skipped") -> { mark = "–"; colorRes = R.color.dim }
                s.optBoolean("ok") -> { mark = "✓"; colorRes = R.color.ok }
                else -> { mark = "✗"; colorRes = R.color.err }
            }
            val label = getString(selfCheckStepLabelRes(name))
            val why = if (s != null && !s.optBoolean("skipped")) {
                selfCheckCodeText(name, s.optString("code"), s.optString("detail"))
            } else ""

            val row = LinearLayout(this)
            row.orientation = LinearLayout.HORIZONTAL
            row.setPadding(0, 8, 0, 8)

            val markView = TextView(this)
            markView.text = mark
            markView.minEms = 2
            markView.setTextColor(ContextCompat.getColor(this, colorRes))

            val textView = TextView(this)
            textView.text = if (why.isNotBlank()) "$label — $why" else label
            textView.setTextColor(ContextCompat.getColor(this, R.color.text))
            textView.textSize = 13.5f

            row.addView(markView)
            row.addView(textView)
            selfCheckSteps.addView(row)
        }
    }

    private fun runSelfCheck() {
        selfCheckStartButton.isEnabled = false
        selfCheckStartButton.setText(R.string.selfcheck_running)
        renderSelfCheckSteps(null)
        val ctx = applicationContext
        Thread {
            val result = try {
                TunnelService.selfCheck(ctx)
            } catch (e: Exception) {
                """{"error":"${e.message}"}"""
            }
            val steps = try {
                val arr = JSONObject(result).optJSONArray("steps")
                if (arr == null) null else (0 until arr.length()).map { arr.getJSONObject(it) }
            } catch (e: Exception) {
                null
            }
            runOnUiThread {
                selfCheckStartButton.isEnabled = true
                selfCheckStartButton.setText(R.string.selfcheck_start)
                renderSelfCheckSteps(steps)
            }
        }.start()
    }

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
        // Сессия — с текущего подключения (её считает ядро и обнуляет при
        // каждом новом старте), «всего» — копится на телефоне между сеансами,
        // см. Settings.adBlockTotal и TunnelService.poll.
        tileBlocked.text = if (running && settings.adBlockEnabled) {
            "${o.optInt("adsBlocked")} (всего ${settings.adBlockTotal})"
        } else {
            "—"
        }

        showPing(if (state == "connected") o.optLong("pingMs") else 0L)
        if (!running) {
            rowSpeed.visibility = View.INVISIBLE
            rowSpeed.removeCallbacks(hideSpeed)
        }
    }
}
