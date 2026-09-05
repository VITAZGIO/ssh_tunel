package io.github.vitazgio.sshtunnel

import android.Manifest
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.view.Gravity
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.AdapterView
import android.widget.ArrayAdapter
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.PopupWindow
import android.widget.ScrollView
import android.widget.Spinner
import android.widget.TextView
import android.widget.Toast
import android.widget.ViewFlipper
import androidx.appcompat.app.AppCompatActivity
import androidx.appcompat.app.AppCompatDelegate
import androidx.core.content.ContextCompat
import androidx.core.os.LocaleListCompat
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
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
    private lateinit var localCheckInfo: View

    private lateinit var languageSpinner: Spinner
    private lateinit var profileTabs: LinearLayout
    private lateinit var addProfileBtn: View
    private lateinit var pasteConfigBtn: Button
    private lateinit var scanConfigBtn: Button
    private lateinit var importNote: TextView
    private lateinit var cityBtn: View
    private lateinit var cityFlagView: FlagView
    private lateinit var cityLabelView: TextView
    private lateinit var customCityName: EditText
    private lateinit var selectServerBtn: Button

    private lateinit var serverPanelHeader: View
    private lateinit var serverPanelChevron: View
    private lateinit var serverPanelBody: View
    private lateinit var generalPanelHeader: View
    private lateinit var generalPanelChevron: View
    private lateinit var generalPanelBody: View

    /** id профиля, который сейчас показан в форме — не обязательно активного. */
    private var editingProfileId: String = ""

    /** Выбранный в выпадающем списке город, либо null — своё имя без флага. */
    private var pickedCity: Cities.City? = null

    private val vpnPermission = registerForActivityResult(
        androidx.activity.result.contract.ActivityResultContracts.StartActivityForResult()
    ) { result ->
        if (result.resultCode == RESULT_OK) start()
    }

    private val notificationPermission = registerForActivityResult(
        androidx.activity.result.contract.ActivityResultContracts.RequestPermission()
    ) { /* отказ не мешает работе, просто не будет уведомления */ }

    private val cameraPermission = registerForActivityResult(
        androidx.activity.result.contract.ActivityResultContracts.RequestPermission()
    ) { granted ->
        if (granted) launchScanner()
        else importNote.text = getString(R.string.camera_denied)
    }

    private val scanLauncher = registerForActivityResult(ScanContract()) { result ->
        val text = result.contents
        if (text != null) applyImportedText(text)
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        settings = Settings(this)
        applyLanguage(settings.language)
        setContentView(R.layout.activity_main)

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
        localCheckInfo = findViewById(R.id.localViaTunnelInfo)

        languageSpinner = findViewById(R.id.languageSpinner)
        profileTabs = findViewById(R.id.profileTabs)
        addProfileBtn = findViewById(R.id.addProfileBtn)
        pasteConfigBtn = findViewById(R.id.pasteConfigBtn)
        scanConfigBtn = findViewById(R.id.scanConfigBtn)
        importNote = findViewById(R.id.importNote)
        cityBtn = findViewById(R.id.cityBtn)
        cityFlagView = findViewById(R.id.cityFlagView)
        cityLabelView = findViewById(R.id.cityLabelView)
        customCityName = findViewById(R.id.customCityName)
        selectServerBtn = findViewById(R.id.selectServerBtn)

        serverPanelHeader = findViewById(R.id.serverPanelHeader)
        serverPanelChevron = findViewById(R.id.serverPanelChevron)
        serverPanelBody = findViewById(R.id.serverPanelBody)
        generalPanelHeader = findViewById(R.id.generalPanelHeader)
        generalPanelChevron = findViewById(R.id.generalPanelChevron)
        generalPanelBody = findViewById(R.id.generalPanelBody)

        editingProfileId = settings.activeProfileId.ifBlank { settings.active().id }
        setupLanguageSpinner()
        setupPanels()
        renderProfileTabs()
        loadProfileIntoForm(settings.active())

        power.setOnClickListener { onToggle() }
        speedButton.setOnClickListener { runSpeedTest() }
        findViewById<Button>(R.id.save).setOnClickListener { saveSettings() }
        findViewById<View>(R.id.apps).setOnClickListener {
            startActivity(Intent(this, AppsActivity::class.java))
        }

        addProfileBtn.setOnClickListener { onAddProfile() }
        cityBtn.setOnClickListener { showCityMenu() }
        customCityName.addOnTextChanged { liveRetitleEditingTab() }
        selectServerBtn.setOnClickListener { onSelectServer() }
        pasteConfigBtn.setOnClickListener { onPasteConfig() }
        scanConfigBtn.setOnClickListener { onScanConfig() }
        localCheckInfo.setOnClickListener { showTip(it, getString(R.string.local_via_tunnel_note)) }

        // Шапка одна на все экраны: логотип всегда возвращает на главную,
        // значки всегда открывают своё. Так не надо помнить, где находишься.
        findViewById<View>(R.id.btnHome).setOnClickListener { flipper.displayedChild = MAIN }
        findViewById<View>(R.id.btnLog).setOnClickListener { flipper.displayedChild = LOG }
        findViewById<View>(R.id.btnSettings).setOnClickListener { openSettings() }

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

    // ---------------------------------------------------------------------
    // Язык интерфейса
    // ---------------------------------------------------------------------

    private fun applyLanguage(lang: String) {
        val current = AppCompatDelegate.getApplicationLocales().toLanguageTags()
        if (current == lang) return
        AppCompatDelegate.setApplicationLocales(LocaleListCompat.forLanguageTags(lang))
    }

    private fun setupLanguageSpinner() {
        val labels = listOf(getString(R.string.lang_ru), getString(R.string.lang_en))
        val codes = listOf("ru", "en")
        languageSpinner.adapter = ArrayAdapter(this, android.R.layout.simple_spinner_item, labels).also {
            it.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item)
        }
        languageSpinner.setSelection(codes.indexOf(settings.language).coerceAtLeast(0))
        languageSpinner.onItemSelectedListener = object : AdapterView.OnItemSelectedListener {
            override fun onItemSelected(parent: AdapterView<*>?, view: View?, pos: Int, id: Long) {
                val lang = codes[pos]
                if (lang != settings.language) {
                    settings.language = lang
                    applyLanguage(lang)
                }
            }
            override fun onNothingSelected(parent: AdapterView<*>?) {}
        }
    }

    // ---------------------------------------------------------------------
    // Сворачиваемые блоки настроек
    // ---------------------------------------------------------------------

    private fun setupPanels() {
        serverPanelHeader.setOnClickListener { togglePanel(isServer = true) }
        generalPanelHeader.setOnClickListener { togglePanel(isServer = false) }
        applyPanelState(serverPanelBody, serverPanelChevron, settings.serverPanelExpanded)
        applyPanelState(generalPanelBody, generalPanelChevron, settings.generalPanelExpanded)
    }

    /** Открыть настройки: в самый первый раз оба блока развёрнуты. */
    private fun openSettings() {
        if (!settings.settingsEverOpened) {
            settings.settingsEverOpened = true
            settings.serverPanelExpanded = true
            settings.generalPanelExpanded = true
            applyPanelState(serverPanelBody, serverPanelChevron, true)
            applyPanelState(generalPanelBody, generalPanelChevron, true)
        }
        flipper.displayedChild = SETTINGS
    }

    private fun togglePanel(isServer: Boolean) {
        val body = if (isServer) serverPanelBody else generalPanelBody
        val chevron = if (isServer) serverPanelChevron else generalPanelChevron
        val expanded = body.visibility != View.VISIBLE
        applyPanelState(body, chevron, expanded)
        if (isServer) settings.serverPanelExpanded = expanded else settings.generalPanelExpanded = expanded
    }

    private fun applyPanelState(body: View, chevron: View, expanded: Boolean) {
        body.visibility = if (expanded) View.VISIBLE else View.GONE
        chevron.rotation = if (expanded) 90f else 0f
    }

    // ---------------------------------------------------------------------
    // Вкладки серверов
    // ---------------------------------------------------------------------

    private fun renderProfileTabs() {
        profileTabs.removeAllViews()
        val active = settings.activeProfileId
        for (p in settings.profiles) {
            val tab = LayoutInflater.from(this).inflate(android.R.layout.simple_list_item_1, profileTabs, false) as TextView
            tab.text = (if (p.flag.isNotBlank()) "⚑ " else "") + p.name
            tab.setPadding(28, 16, 28, 16)
            tab.setTextColor(ContextCompat.getColor(this, R.color.text))
            tab.textSize = 12.5f
            tab.setBackgroundResource(if (p.id == editingProfileId) R.drawable.bg_button_primary else R.drawable.bg_button)
            if (p.id == active) tab.text = "● " + tab.text
            val lp = LinearLayout.LayoutParams(LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT)
            lp.marginEnd = 6
            tab.layoutParams = lp
            tab.setOnClickListener { onTabClicked(p.id) }
            profileTabs.addView(tab)
        }
    }

    private fun onTabClicked(id: String) {
        if (id == editingProfileId) return
        saveCurrentFormInto(editingProfileId)
        editingProfileId = id
        val p = settings.profiles.find { it.id == id } ?: settings.active()
        loadProfileIntoForm(p)
        renderProfileTabs()
    }

    private fun onAddProfile() {
        saveCurrentFormInto(editingProfileId)
        val p = settings.addProfile("", "")
        editingProfileId = p.id
        loadProfileIntoForm(p)
        renderProfileTabs()
    }

    private fun onSelectServer() {
        saveCurrentFormInto(editingProfileId)
        if (editingProfileId == settings.activeProfileId) {
            Toast.makeText(this, R.string.server_switched, Toast.LENGTH_SHORT).show()
            return
        }
        if (TunnelService.state != "stopped") {
            startService(Intent(this, TunnelService::class.java).setAction(TunnelService.ACTION_STOP))
            Toast.makeText(this, R.string.tunnel_stopped_for_switch, Toast.LENGTH_LONG).show()
        }
        settings.setActive(editingProfileId)
        renderProfileTabs()
        Toast.makeText(this, R.string.server_switched, Toast.LENGTH_SHORT).show()
        refresh()
    }

    // ---------------------------------------------------------------------
    // Выбор города/флага
    // ---------------------------------------------------------------------

    private fun showCityMenu() {
        val list = LinearLayout(this)
        list.orientation = LinearLayout.VERTICAL
        val scroll = ScrollView(this)
        scroll.addView(list)
        scroll.layoutParams = LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, dp(280)
        )

        for (city in Cities.ALL) {
            list.addView(cityRow(FlagViewIcon(city.code), Cities.label(city, settings.language)) {
                pickedCity = city
                renderCityBtn()
                customCityName.visibility = View.GONE
                liveRetitleEditingTab()
            })
        }
        list.addView(cityRow(FlagViewIcon(""), getString(R.string.custom_flag_name)) {
            pickedCity = null
            renderCityBtn()
            customCityName.visibility = View.VISIBLE
            customCityName.requestFocus()
            liveRetitleEditingTab()
        })

        val popup = PopupWindow(scroll, cityBtn.width, ViewGroup.LayoutParams.WRAP_CONTENT, true)
        popup.setBackgroundDrawable(ContextCompat.getDrawable(this, R.drawable.bg_card))
        popup.elevation = 12f
        popup.showAsDropDown(cityBtn, 0, 4)
    }

    private fun FlagViewIcon(code: String): FlagView {
        val fv = FlagView(this)
        fv.code = code
        fv.layoutParams = LinearLayout.LayoutParams(dp(26), dp(18))
        return fv
    }

    private fun cityRow(flag: FlagView, label: String, onClick: () -> Unit): View {
        val row = LinearLayout(this)
        row.orientation = LinearLayout.HORIZONTAL
        row.gravity = Gravity.CENTER_VERTICAL
        row.setPadding(dp(12), dp(10), dp(12), dp(10))
        row.isClickable = true
        row.isFocusable = true
        row.addView(flag)
        val text = TextView(this)
        text.text = label
        text.setTextColor(ContextCompat.getColor(this, R.color.text))
        text.textSize = 13.5f
        val lp = LinearLayout.LayoutParams(LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT)
        lp.marginStart = dp(10)
        text.layoutParams = lp
        row.addView(text)
        row.setOnClickListener { onClick() }
        return row
    }

    private fun dp(v: Int): Int = (v * resources.displayMetrics.density).toInt()

    private fun renderCityBtn() {
        val city = pickedCity
        if (city == null) {
            cityFlagView.code = ""
            cityLabelView.text = getString(R.string.custom_flag_name)
        } else {
            cityFlagView.code = city.code
            cityLabelView.text = Cities.label(city, settings.language)
        }
    }

    private fun liveRetitleEditingTab() {
        // Подпись вкладки обновится при следующем renderProfileTabs() —
        // после сохранения формы (переключение вкладки, «Выбрать этот
        // сервер» или «Сохранить»). Здесь достаточно того, что renderCityBtn
        // уже показал выбор в самой кнопке.
    }

    // ---------------------------------------------------------------------
    // Загрузка и сохранение формы
    // ---------------------------------------------------------------------

    private fun loadProfileIntoForm(p: Settings.Profile) {
        hostEdit.setText(p.host)
        portEdit.setText(p.sshPort.toString())
        poolEdit.setText(p.poolSize.toString())
        userEdit.setText(p.user)
        keyEdit.setText("")
        directEdit.setText(p.directHosts)
        localCheck.isChecked = p.localViaTunnel
        keyStateView.setText(if (settings.keyFile(p.id).let { it.exists() && it.length() > 0 }) R.string.key_saved else R.string.key_missing)
        appsSummary.text = appsSummaryText(p)

        val city = Cities.forProfile(p.name, p.flag)
        pickedCity = city
        renderCityBtn()
        customCityName.visibility = if (city == null) View.VISIBLE else View.GONE
        customCityName.setText(if (city == null) p.name else "")
    }

    /** Переносит текущие поля формы в профиль с указанным id и сохраняет его. */
    private fun saveCurrentFormInto(id: String) {
        val list = settings.profiles
        val p = list.find { it.id == id } ?: return
        val city = pickedCity
        if (city != null) {
            p.name = Cities.label(city, settings.language)
            p.flag = city.code
        } else {
            p.name = customCityName.text.toString().trim().ifBlank { p.name.ifBlank { "Server" } }
            p.flag = ""
        }
        p.host = hostEdit.text.toString().trim()
        p.sshPort = portEdit.text.toString().toIntOrNull() ?: 22
        p.poolSize = poolEdit.text.toString().toIntOrNull() ?: 4
        p.user = userEdit.text.toString().trim()
        p.directHosts = directEdit.text.toString()
        p.localViaTunnel = localCheck.isChecked
        settings.saveProfile(p)

        val key = keyEdit.text.toString()
        if (key.isNotBlank()) {
            settings.saveKeyFor(id, key)
            keyEdit.setText("")
        }
    }

    private fun saveSettings() {
        saveCurrentFormInto(editingProfileId)
        renderProfileTabs()
        loadProfileIntoForm(settings.profiles.find { it.id == editingProfileId } ?: settings.active())
        refresh()
        Toast.makeText(this, R.string.saved, Toast.LENGTH_SHORT).show()
    }

    // ---------------------------------------------------------------------
    // Настройка из буфера обмена или QR-кода (ядро на Go разбирает текст)
    // ---------------------------------------------------------------------

    private fun onPasteConfig() {
        val cm = getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager
        val text = cm?.primaryClip?.takeIf { it.itemCount > 0 }
            ?.getItemAt(0)?.coerceToText(this)?.toString().orEmpty()
        if (text.isBlank()) {
            importNote.text = getString(R.string.paste_failed)
            return
        }
        applyImportedText(text)
    }

    private fun onScanConfig() {
        val granted = ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA) ==
            PackageManager.PERMISSION_GRANTED
        if (granted) launchScanner() else cameraPermission.launch(Manifest.permission.CAMERA)
    }

    private fun launchScanner() {
        scanLauncher.launch(
            ScanOptions()
                .setOrientationLocked(true)
                .setBeepEnabled(false)
                .setDesiredBarcodeFormats(ScanOptions.QR_CODE)
        )
    }

    /**
     * Разбирает текст (из буфера или QR-кода) через то же ядро на Go, что
     * читает файлы экспорта на компьютере, и создаёт из него новый сервер.
     * Если текст не похож на конфиг ssh_tunnel — ничего не меняем, только
     * показываем ошибку на месте.
     */
    private fun applyImportedText(text: String) {
        val parsed = try {
            mobile.Mobile.parseConfig(text)
        } catch (e: Exception) {
            importNote.text = e.message ?: getString(R.string.import_bad_apps)
            return
        }

        // Явные getX(): gomobile сам решает регистр синтетических свойств
        // Kotlin для полей вроде SSHPort, полагаться на него не стоит.
        val name = parsed.getName()
        val flag = parsed.getFlag()
        val host = parsed.getHost()
        val sshPort = parsed.getSshPort()
        val user = parsed.getUser()
        val poolSize = parsed.getPoolSize()
        val filterMode = parsed.getFilterMode()
        val filterApps = parsed.getFilterApps()
        val directHosts = parsed.getDirectHosts()
        val localViaTunnel = parsed.getLocalViaTunnel()
        val keyIncluded = parsed.getKeyIncluded()
        val keyContents = parsed.getKeyContents()

        saveCurrentFormInto(editingProfileId)
        val p = settings.addProfile(name, flag)
        p.host = host
        p.sshPort = if (sshPort > 0) sshPort else 22
        p.user = user.ifBlank { "tunnel" }
        p.poolSize = if (poolSize > 0) poolSize else 4
        p.filterMode = filterMode.ifBlank { "all" }
        p.filterApps = filterApps.split("\n").map { it.trim() }.filter { it.isNotBlank() }.toMutableSet()
        p.directHosts = directHosts
        p.localViaTunnel = localViaTunnel
        settings.saveProfile(p)

        if (keyIncluded && keyContents.isNotBlank()) {
            settings.saveKeyFor(p.id, keyContents)
        }

        if (TunnelService.state != "stopped") {
            startService(Intent(this, TunnelService::class.java).setAction(TunnelService.ACTION_STOP))
        }
        settings.setActive(p.id)
        editingProfileId = p.id
        renderProfileTabs()
        loadProfileIntoForm(p)
        importNote.text = getString(if (keyIncluded) R.string.import_ok else R.string.import_ok_no_key)
        refresh()
    }

    // ---------------------------------------------------------------------
    // Всплывающая подсказка по значку «?» — как .infoq в панели на компьютере.
    // ---------------------------------------------------------------------

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

    private fun onToggle() {
        if (TunnelService.state != "stopped") {
            startService(Intent(this, TunnelService::class.java).setAction(TunnelService.ACTION_STOP))
            return
        }
        if (!settings.ready()) {
            Toast.makeText(this, R.string.need_settings, Toast.LENGTH_LONG).show()
            openSettings()
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
        appsSummary.text = appsSummaryText(settings.active())

        showStats(state)

        val lines = synchronized(TunnelService.log) { TunnelService.log.toList() }
        logView.text = lines.joinToString("\n")
        logScroll.post { logScroll.fullScroll(View.FOCUS_DOWN) }
    }

    private fun appsSummaryText(p: Settings.Profile): String = when (p.filterMode) {
        "only" -> getString(R.string.mode_only) + ", " +
            getString(R.string.apps_chosen, p.filterApps.size)
        "except" -> getString(R.string.mode_except) + ", " +
            getString(R.string.apps_chosen, p.filterApps.size)
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
            rowSpeed.visibility = View.INVISIBLE
            rowSpeed.removeCallbacks(hideSpeed)
        }
    }
}

/** Небольшой помощник: слушатель изменения текста без лишних методов интерфейса. */
private fun EditText.addOnTextChanged(onChanged: () -> Unit) {
    addTextChangedListener(object : android.text.TextWatcher {
        override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) {}
        override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {}
        override fun afterTextChanged(s: android.text.Editable?) = onChanged()
    })
}
