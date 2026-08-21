package io.github.vitazgio.sshtunnel

import android.content.Intent
import android.graphics.drawable.Icon
import android.net.VpnService
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService

/**
 * Кнопка в шторке быстрых настроек.
 *
 * Включать туннель ради двух нажатий на значок и кнопку — лишняя работа,
 * особенно если делаешь это по десять раз в день. Отсюда он включается прямо
 * из шторки, не открывая приложение.
 *
 * Исключение одно: если система ещё не спрашивала разрешение на
 * VPN-подключение, спросить его из шторки нельзя — там нет окна. Тогда
 * открываем приложение, и человек подтверждает как обычно.
 */
class TunnelTile : TileService() {

    override fun onStartListening() {
        super.onStartListening()
        sync()
        // Пока шторка открыта, состояние может измениться — например, туннель
        // отвалился. Слушаем те же обновления, что и экран.
        TunnelService.onUpdate = { sync() }
    }

    override fun onStopListening() {
        TunnelService.onUpdate = null
        super.onStopListening()
    }

    override fun onClick() {
        super.onClick()

        if (TunnelService.state != "stopped") {
            startService(
                Intent(this, TunnelService::class.java).setAction(TunnelService.ACTION_STOP)
            )
            sync()
            return
        }

        if (!Settings(this).ready() || VpnService.prepare(this) != null) {
            openApp()
            return
        }

        startService(
            Intent(this, TunnelService::class.java).setAction(TunnelService.ACTION_START)
        )
        sync()
    }

    private fun openApp() {
        val intent = Intent(this, MainActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            val pending = android.app.PendingIntent.getActivity(
                this, 0, intent,
                android.app.PendingIntent.FLAG_UPDATE_CURRENT or
                    android.app.PendingIntent.FLAG_IMMUTABLE,
            )
            startActivityAndCollapse(pending)
        } else {
            @Suppress("DEPRECATION")
            startActivityAndCollapse(intent)
        }
    }

    /** Приводит вид кнопки в соответствие с тем, что на самом деле происходит. */
    private fun sync() {
        val tile = qsTile ?: return
        val state = TunnelService.state

        tile.state = when (state) {
            "connected" -> Tile.STATE_ACTIVE
            "stopped" -> Tile.STATE_INACTIVE
            else -> Tile.STATE_UNAVAILABLE // подключается или чинится
        }
        tile.label = getString(R.string.app_name)
        tile.icon = Icon.createWithResource(this, R.drawable.ic_notify)
        if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.Q) {
            tile.subtitle = getString(
                when (state) {
                    "connected" -> R.string.state_connected
                    "connecting" -> R.string.state_connecting
                    "reconnecting" -> R.string.state_reconnecting
                    "error" -> R.string.state_error
                    else -> R.string.state_stopped
                }
            )
        }
        tile.updateTile()
    }
}
