package io.github.vitazgio.sshtunnel

import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.os.Bundle
import android.widget.ArrayAdapter
import android.widget.ListView
import android.widget.RadioGroup
import androidx.appcompat.app.AppCompatActivity

/**
 * Выбор приложений.
 *
 * На телефоне отбором занимается сама система: какие приложения заводить в
 * туннель, задаётся при создании подключения и дальше соблюдается ядром
 * Android. Наше дело — только собрать список.
 *
 * Отдельный смысл у режима «все, кроме выбранных»: звонкам и играм нужен UDP,
 * который через SSH не проходит. Вынесенное сюда работает как обычно.
 */
class AppsActivity : AppCompatActivity() {

    private lateinit var settings: Settings
    private lateinit var list: ListView
    private var packages: List<String> = emptyList()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_apps)
        settings = Settings(this)

        list = findViewById(R.id.list)
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

        fillList()
    }

    private fun fillList() {
        val pm = packageManager
        val installed = pm.getInstalledApplications(PackageManager.GET_META_DATA)
            // Приложения без выхода в сеть в этом списке только мешают.
            .filter { pm.checkPermission(android.Manifest.permission.INTERNET, it.packageName) == PackageManager.PERMISSION_GRANTED }
            .filter { it.packageName != packageName }
            .sortedBy { label(pm, it).lowercase() }

        packages = installed.map { it.packageName }
        val labels = installed.map { label(pm, it) }

        list.adapter = ArrayAdapter(this, android.R.layout.simple_list_item_multiple_choice, labels)

        val chosen = settings.filterApps
        packages.forEachIndexed { i, pkg ->
            if (pkg in chosen) list.setItemChecked(i, true)
        }

        list.setOnItemClickListener { _, _, _, _ -> saveChosen() }
    }

    private fun saveChosen() {
        val chosen = mutableSetOf<String>()
        val checked = list.checkedItemPositions
        for (i in packages.indices) {
            if (checked.get(i, false)) chosen.add(packages[i])
        }
        settings.filterApps = chosen
    }

    private fun label(pm: PackageManager, info: ApplicationInfo): String =
        pm.getApplicationLabel(info).toString()

    override fun onPause() {
        saveChosen()
        super.onPause()
    }
}
