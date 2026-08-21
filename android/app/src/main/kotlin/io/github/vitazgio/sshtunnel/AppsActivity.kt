package io.github.vitazgio.sshtunnel

import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.graphics.drawable.Drawable
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
import androidx.appcompat.app.AppCompatActivity

/**
 * Выбор приложений.
 *
 * На телефоне отбором занимается сама система: какие приложения заводить в
 * туннель, задаётся при создании подключения и дальше соблюдается ядром
 * Android. Наше дело — собрать список.
 *
 * Отдельный смысл у режима «все, кроме выбранных»: звонкам и играм нужен UDP,
 * который через SSH не проходит. Вынесенное сюда работает как обычно.
 */
class AppsActivity : AppCompatActivity() {

    private class App(val pkg: String, val label: String, val icon: Drawable?)

    private lateinit var settings: Settings
    private lateinit var list: ListView
    private var apps: List<App> = emptyList()

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
