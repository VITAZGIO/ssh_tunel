package io.github.vitazgio.sshtunnel

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.graphics.drawable.Drawable
import android.net.Uri
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.ArrayAdapter
import android.widget.CheckBox
import android.widget.ImageView
import android.widget.ListView
import android.widget.RadioGroup
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import org.json.JSONArray
import org.json.JSONObject
import java.io.BufferedReader
import java.io.InputStreamReader

/**
 * Выбор приложений.
 *
 * На телефоне отбором занимается сама система: какие приложения заводить в
 * туннель, задаётся при создании подключения и дальше соблюдается ядром
 * Android. Наше дело — собрать список.
 *
 * Отдельный смысл у режима «все, кроме выбранных»: звонкам и играм нужен UDP,
 * который через SSH не проходит. Вынесенное сюда работает как обычно.
 *
 * Список умеет экспорт и импорт тем же форматом JSON, что и панель на
 * компьютере (см. appsExportDoc в index.html): {"sshTunnelAppList":1,
 * "filterMode":..., "apps":[{"name":...,"path":...}]}. На Android в name
 * кладётся имя пакета, path остаётся пустым — лишние поля при импорте
 * игнорируются, формат общий.
 */
class AppsActivity : AppCompatActivity() {

    private class App(val pkg: String, val label: String, val icon: Drawable?)

    private lateinit var settings: Settings
    private lateinit var list: ListView
    private lateinit var note: TextView
    private var apps: List<App> = emptyList()

    private val createDocument = registerForActivityResult(
        ActivityResultContracts.CreateDocument("application/json")
    ) { uri -> if (uri != null) writeExportTo(uri) }

    private val openDocument = registerForActivityResult(
        ActivityResultContracts.OpenDocument()
    ) { uri -> if (uri != null) readImportFrom(uri) }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_apps)
        settings = Settings(this)

        // Отступы под полосу часов и вырез камеры: с Android 15 окно занимает
        // экран целиком, и без этого заголовок оказывается под ними.
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

        list = findViewById(R.id.list)
        note = findViewById(R.id.appsNote)
        findViewById<View>(R.id.backFromApps).setOnClickListener { finish() }

        val mode = findViewById<RadioGroup>(R.id.mode)
        mode.check(
            when (settings.filterMode) {
                "only" -> R.id.modeOnly
                "except" -> R.id.modeExcept
                else -> R.id.modeAll
            }
        )
        mode.setOnCheckedChangeListener { _, checked ->
            settings.filterMode = when (checked) {
                R.id.modeOnly -> "only"
                R.id.modeExcept -> "except"
                else -> "all"
            }
        }

        findViewById<View>(R.id.exportAppsBtn).setOnClickListener { exportApps() }
        findViewById<View>(R.id.importAppsBtn).setOnClickListener {
            openDocument.launch(arrayOf("application/json", "text/plain", "*/*"))
        }
        findViewById<View>(R.id.pasteAppsBtn).setOnClickListener { pasteApps() }

        fillList()
    }

    /**
     * Список того, что человек видит у себя на телефоне.
     *
     * Отбор по одному признаку: у приложения есть значок запуска. Всё
     * остальное — службы системы, поставщики данных и прочая начинка, которой
     * в списке не место: её там сотни, и решать про неё нечего.
     */
    private fun fillList() {
        val pm = packageManager
        val chosen = settings.filterApps

        val found = pm.getInstalledApplications(0)
            .filter { pm.getLaunchIntentForPackage(it.packageName) != null }
            .filter { it.packageName != packageName }
            .map { App(it.packageName, label(pm, it), icon(pm, it)) }

        // Выбранные наверх — иначе после десятка отметок их не найти.
        apps = found.sortedWith(
            compareByDescending<App> { it.pkg in chosen }.thenBy { it.label.lowercase() }
        )

        list.adapter = Adapter()
        apps.forEachIndexed { i, app ->
            if (app.pkg in chosen) list.setItemChecked(i, true)
        }
        list.setOnItemClickListener { _, _, _, _ -> saveChosen() }
    }

    private inner class Adapter : ArrayAdapter<App>(
        this, R.layout.item_app, R.id.appName, apps
    ) {
        override fun getView(position: Int, convertView: View?, parent: ViewGroup): View {
            val view = convertView ?: LayoutInflater.from(context)
                .inflate(R.layout.item_app, parent, false)
            val app = apps[position]
            view.findViewById<TextView>(R.id.appName).text = app.label
            view.findViewById<ImageView>(R.id.appIcon).setImageDrawable(app.icon)
            view.findViewById<CheckBox>(R.id.appCheck).isChecked =
                list.checkedItemPositions.get(position, false)
            return view
        }
    }

    private fun saveChosen() {
        val checked = list.checkedItemPositions
        val chosen = mutableSetOf<String>()
        for (i in apps.indices) {
            if (checked.get(i, false)) chosen.add(apps[i].pkg)
        }
        settings.filterApps = chosen
        // Галочки рисуем сами, поэтому список надо попросить перерисоваться.
        (list.adapter as ArrayAdapter<*>).notifyDataSetChanged()
    }

    // -----------------------------------------------------------------
    // Экспорт и импорт списка — тот же формат JSON, что на компьютере.
    // -----------------------------------------------------------------

    private fun exportDocText(): String {
        val doc = JSONObject()
        doc.put("sshTunnelAppList", 1)
        doc.put("filterMode", settings.filterMode)
        val arr = JSONArray()
        for (pkg in settings.filterApps) {
            val item = JSONObject()
            item.put("name", pkg)
            item.put("path", "")
            arr.put(item)
        }
        doc.put("apps", arr)
        return doc.toString(2)
    }

    private fun exportApps() {
        copyToClipboard(exportDocText())
        createDocument.launch("ssh_tunnel-apps.json")
    }

    private fun writeExportTo(uri: Uri) {
        try {
            contentResolver.openOutputStream(uri)?.use { out ->
                out.write(exportDocText().toByteArray(Charsets.UTF_8))
            }
            note.text = getString(R.string.apps_exported)
        } catch (e: Exception) {
            note.text = e.message ?: getString(R.string.import_bad_apps)
        }
    }

    private fun pasteApps() {
        val cm = getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager
        val text = cm?.primaryClip?.takeIf { it.itemCount > 0 }
            ?.getItemAt(0)?.coerceToText(this)?.toString().orEmpty()
        if (text.isBlank()) {
            note.text = getString(R.string.paste_failed)
            return
        }
        applyImportText(text)
    }

    private fun readImportFrom(uri: Uri) {
        try {
            val text = contentResolver.openInputStream(uri)?.use { input ->
                BufferedReader(InputStreamReader(input, Charsets.UTF_8)).readText()
            } ?: ""
            applyImportText(text)
        } catch (e: Exception) {
            note.text = e.message ?: getString(R.string.import_bad_apps)
        }
    }

    /**
     * Принимает и наш формат {apps:[...]}, и просто голый массив — то же
     * правило, что и на компьютере (appsListFrom в index.html). Если это
     * вообще не список программ — ничего не меняем, только показываем
     * ошибку. Импорт добавляет к текущему списку, а не затирает его.
     */
    private fun applyImportText(text: String) {
        val trimmed = text.trim()
        if (trimmed.isBlank()) {
            note.text = getString(R.string.import_bad_apps)
            return
        }
        val names = try {
            appNamesFrom(trimmed)
        } catch (e: Exception) {
            null
        }
        if (names == null || names.isEmpty()) {
            note.text = getString(R.string.import_bad_apps)
            return
        }
        val chosen = settings.filterApps.toMutableSet()
        chosen.addAll(names)
        settings.filterApps = chosen
        fillList()
        note.text = getString(R.string.apps_imported)
    }

    private fun appNamesFrom(text: String): List<String>? {
        val root = try {
            JSONObject(text)
        } catch (e: Exception) {
            null
        }
        val arr = when {
            root != null && root.has("apps") -> root.optJSONArray("apps")
            else -> try { JSONArray(text) } catch (e: Exception) { null }
        } ?: return null

        val out = mutableListOf<String>()
        for (i in 0 until arr.length()) {
            when (val item = arr.get(i)) {
                is String -> if (item.isNotBlank()) out.add(item)
                is JSONObject -> {
                    val name = item.optString("name")
                    if (name.isNotBlank()) out.add(name)
                }
                else -> {}
            }
        }
        return out
    }

    private fun copyToClipboard(text: String) {
        val cm = getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager
        cm?.setPrimaryClip(ClipData.newPlainText("ssh_tunnel apps", text))
    }

    private fun label(pm: PackageManager, info: ApplicationInfo): String =
        pm.getApplicationLabel(info).toString()

    private fun icon(pm: PackageManager, info: ApplicationInfo): Drawable? = try {
        pm.getApplicationIcon(info)
    } catch (e: Exception) {
        null
    }

    override fun onPause() {
        saveChosen()
        super.onPause()
    }
}
