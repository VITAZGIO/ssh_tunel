package io.github.vitazgio.sshtunnel

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.RadioGroup
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import org.json.JSONObject

/**
 * Новый пользователь прямо на сервере, к которому подключён туннель, — как
 * вкладка «Пользователь» мастера настройки на компьютере. Работает, если в
 * панели сервера этому устройству включено «Может создавать пользователей»;
 * пароль — тот, что задан там. Сервер узнаёт нас по SSH-пользователю
 * туннеля (см. internal/serverusers).
 */
class UsersActivity : AppCompatActivity() {

    private lateinit var whoView: TextView
    private lateinit var nameEdit: EditText
    private lateinit var typeGroup: RadioGroup
    private lateinit var passEdit: EditText
    private lateinit var createButton: Button
    private lateinit var statusView: TextView
    private lateinit var resultBox: View

    /** Настройки нового пользователя (JSON файла обмена) — для «Скопировать»/«Поделиться». */
    private var config = ""

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        Look.apply(this)
        setContentView(R.layout.activity_users)

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
        findViewById<View>(R.id.backFromUsers).setOnClickListener { finish() }

        whoView = findViewById(R.id.usersWho)
        nameEdit = findViewById(R.id.usersName)
        typeGroup = findViewById(R.id.usersType)
        passEdit = findViewById(R.id.usersPass)
        createButton = findViewById(R.id.usersCreate)
        statusView = findViewById(R.id.usersStatus)
        resultBox = findViewById(R.id.usersResult)

        createButton.setOnClickListener { create() }
        findViewById<View>(R.id.usersCopy).setOnClickListener {
            val cm = getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager
            cm?.setPrimaryClip(ClipData.newPlainText(getString(R.string.users_title), config))
            Toast.makeText(this, R.string.users_copied, Toast.LENGTH_SHORT).show()
        }
        findViewById<View>(R.id.usersShare).setOnClickListener {
            val send = Intent(Intent.ACTION_SEND).apply {
                type = "text/plain"
                putExtra(Intent.EXTRA_TEXT, config)
            }
            startActivity(Intent.createChooser(send, getString(R.string.users_share)))
        }

        askWho()
    }

    /** Спросить сервер, кто мы для него и можно ли нам создавать. */
    private fun askWho() {
        if (TunnelService.state != "connected") {
            whoView.setText(R.string.need_connected)
            createButton.isEnabled = false
            return
        }
        whoView.setText(R.string.users_checking)
        Thread {
            val out = call("whoami", "", "", "")
            runOnUiThread {
                val err = out.optString("error")
                whoView.text = when {
                    err.isNotEmpty() -> err
                    out.optBoolean("canCreate") -> getString(R.string.users_can, out.optString("name"))
                    else -> getString(R.string.users_cannot, out.optString("name"))
                }
            }
        }.start()
    }

    private fun create() {
        val name = nameEdit.text.toString().trim()
        if (name.isEmpty()) {
            statusView.setText(R.string.users_need_name)
            return
        }
        val type = when (typeGroup.checkedRadioButtonId) {
            R.id.usersTypeWindows -> "windows"
            R.id.usersTypeLinux -> "linux"
            else -> "android"
        }
        val pass = passEdit.text.toString()
        createButton.isEnabled = false
        resultBox.visibility = View.GONE
        statusView.setText(R.string.users_creating)
        Thread {
            val out = call("create", name, type, pass)
            runOnUiThread {
                createButton.isEnabled = true
                val err = out.optString("error")
                if (err.isNotEmpty()) {
                    statusView.text = err
                    return@runOnUiThread
                }
                passEdit.setText("")
                config = out.optString("config")
                statusView.text = getString(R.string.users_created, out.optString("name", name))
                resultBox.visibility = View.VISIBLE
            }
        }.start()
    }

    private fun call(op: String, name: String, type: String, pass: String): JSONObject = try {
        JSONObject(TunnelService.serverUsers(op, name, type, pass))
    } catch (e: Exception) {
        JSONObject().put("error", e.message ?: e.toString())
    }
}
