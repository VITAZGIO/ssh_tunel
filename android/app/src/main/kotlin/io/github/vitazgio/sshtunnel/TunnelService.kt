package io.github.vitazgio.sshtunnel

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.util.Log
import mobile.Callbacks
import mobile.Mobile
import java.net.InetAddress

/**
 * Служба туннеля.
 *
 * Здесь происходит то, чего нет и не может быть на компьютере: система отдаёт
 * виртуальный сетевой интерфейс, в который сыплются сырые IP-пакеты, а разбирать
 * их и превращать обратно в соединения — забота ядра на Go.
 */
class TunnelService : VpnService(), Callbacks {

    companion object {
        const val ACTION_START = "io.github.vitazgio.sshtunnel.START"
        const val ACTION_STOP = "io.github.vitazgio.sshtunnel.STOP"

        /** Состояние и журнал для экрана: служба живёт отдельно от него. */
        @Volatile var state: String = "stopped"
        @Volatile var detail: String = ""
        @Volatile var statsJson: String = "{}"
        val log = ArrayDeque<String>()

        var onUpdate: (() -> Unit)? = null

        /** Работающая служба — через неё экран запускает тест скорости. */
        @Volatile private var current: TunnelService? = null

        fun speedTest(): String =
            current?.tunnel?.speedTest() ?: """{"error":"туннель выключен"}"""

        private const val TAG = "ssh_tunnel"
        private const val CHANNEL = "tunnel"
        private const val NOTIFICATION_ID = 1
        private const val MTU = 1500
    }

    private var fd: ParcelFileDescriptor? = null
    private val tunnel = Mobile.newTunnel()

    /** Гашение уже идёт: второй раз ядро останавливать не надо. */
    @Volatile private var stopping = false

    private val ticker = android.os.Handler(android.os.Looper.getMainLooper())

    /**
     * Опрос состояния у ядра.
     *
     * События с состоянием приходят сами, но полагаться только на них нельзя:
     * одно потерянное сообщение — и экран навсегда застрял на «подключение…»,
     * хотя туннель работает. Опрос это исключает и заодно даёт живые счётчики.
     */
    private val poll = object : Runnable {
        override fun run() {
            val now = try {
                tunnel.state()
            } catch (e: Exception) {
                "error"
            }
            if (now != state) {
                report(now, detail)
            }
            statsJson = try { tunnel.statsJSON() } catch (e: Exception) { "{}" }
            onUpdate?.invoke()
            ticker.postDelayed(this, 1500)
        }
    }

    override fun onCreate() {
        super.onCreate()
        current = this
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopTunnel()
            return START_NOT_STICKY
        }
        startTunnel()
        return START_STICKY
    }

    private fun startTunnel() {
        val settings = Settings(this)
        if (!settings.ready()) {
            report("error", "не заполнены настройки")
            stopSelf()
            return
        }

        stopping = false
        startForeground(NOTIFICATION_ID, notification("подключение…"))
        report("connecting", "")

        // В отдельном потоке: подключение к серверу и проверка его возможностей
        // занимают секунды, а держать всё это время главный поток нельзя.
        Thread {
            try {
                tunnel.setCallbacks(this)
                tunnel.configure(
                    settings.host,
                    settings.sshPort.toLong(),
                    settings.user,
                    settings.keyFile.absolutePath,
                    settings.knownHostsFile.absolutePath,
                    settings.poolSize.toLong(),
                    settings.directHosts,
                    settings.localViaTunnel,
                )
                tunnel.startCore()

                // Сначала спрашиваем сервер, умеет ли он IPv6, и только потом
                // создаём интерфейс. Объявить телефону шестую версию и не суметь
                // её обслужить — худшее из возможного: приложения дружно уходят
                // туда, и не работает вообще ничего. Ровно это и случилось на
                // первой живой проверке.
                val hasIPv6 = tunnel.serverHasIPv6()
                onLog(
                    if (hasIPv6) "сервер умеет IPv6 — ведём его через туннель"
                    else "сервер без IPv6 — приложения пойдут по IPv4"
                )

                val iface = buildInterface(settings, hasIPv6)
                fd = iface
                // detachFd, а не getFd: дальше дескриптором распоряжается Go и
                // закрывает его сам. Иначе его закрыли бы дважды.
                tunnel.startStack(iface.detachFd().toLong(), MTU.toLong())

                ticker.post(poll)
            } catch (e: Exception) {
                // Нажали «стоп», не дождавшись подключения, — это не ошибка, и
                // красной надписи здесь быть не должно.
                if (stopping) return@Thread
                val why = e.message ?: "не удалось запустить туннель"
                ticker.post { stopTunnel("error", why) }
            }
        }.start()
    }

    /**
     * Собирает виртуальный интерфейс.
     *
     * Ключевых мест два. Первое — маршруты: локальные сети в туннель не
     * заводятся вовсе, поэтому домашние сервисы остаются доступными без единой
     * проверки в коде. Второе — отбор приложений: за него отвечает сама
     * система, и это надёжнее любых наших правил.
     */
    private fun buildInterface(settings: Settings, withIPv6: Boolean): ParcelFileDescriptor {
        val builder = Builder()
            .setSession(getString(R.string.app_name))
            .setMtu(MTU)
            // Адрес самого интерфейса. Из того же диапазона, что и подставные
            // адреса, — настоящих сайтов там нет.
            .addAddress("198.18.0.1", 15)
            // DNS-сервер — отдельный адрес, не адрес самого интерфейса:
            // пакет на собственный адрес система может обработать сама и в
            // туннель не отдать. Тогда имена не разрешаются вообще.
            .addDnsServer(Mobile.dnsAddr())

        // Весь трафик — в туннель. Дальше из него вычитается локальная сеть.
        builder.addRoute("0.0.0.0", 0)

        // IPv6 объявляем ровно тогда, когда сервер умеет его обслужить.
        //
        // Если не объявлять, Android сам блокирует шестую версию и приложения
        // выбирают четвёртую — она через туннель работает. Если объявить, не
        // умея, получается наоборот: приложения уходят в IPv6 и упираются в
        // «open failed» на каждом соединении.
        if (withIPv6) {
            builder.addAddress("fd00:c0de:7075::1", 64)
            builder.addRoute("::", 0)
        }

        if (!settings.localViaTunnel) {
            excludeLocalNetworks(builder)
        }

        applyAppFilter(builder, settings)

        builder.setConfigureIntent(
            PendingIntent.getActivity(
                this, 0, Intent(this, MainActivity::class.java),
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
            )
        )

        return builder.establish()
            ?: throw IllegalStateException("система не выдала интерфейс")
    }

    /**
     * Убирает локальные сети из туннеля.
     *
     * Начиная с Android 13 для этого есть готовый способ. На более старых
     * версиях приходится перечислять маршруты вручную, поэтому там локальная
     * сеть остаётся в туннеле — иначе список маршрутов пришлось бы считать как
     * дополнение диапазонов, и одна ошибка в нём отрезала бы связь целиком.
     */
    private fun excludeLocalNetworks(builder: Builder) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) {
            Log.i(TAG, "Android старее 13: локальная сеть останется в туннеле")
            return
        }
        val local = listOf(
            "10.0.0.0" to 8,
            "172.16.0.0" to 12,
            "192.168.0.0" to 16,
            "169.254.0.0" to 16, // адреса без DHCP
            "100.64.0.0" to 10,  // сети mesh-VPN
        )
        for ((addr, prefix) in local) {
            try {
                builder.excludeRoute(android.net.IpPrefix(InetAddress.getByName(addr), prefix))
            } catch (e: Exception) {
                Log.w(TAG, "не удалось исключить $addr/$prefix: ${e.message}")
            }
        }
    }

    /**
     * Отбор приложений средствами системы.
     *
     * Смешивать разрешённые и запрещённые в одном подключении нельзя, поэтому
     * режимы взаимно исключающие — те же три, что и в настройках на компьютере.
     * Себя в туннель не заводим никогда: соединение с сервером должно идти мимо.
     */
    private fun applyAppFilter(builder: Builder, settings: Settings) {
        val apps = settings.filterApps
        when (settings.filterMode) {
            "only" -> {
                if (apps.isEmpty()) return
                for (pkg in apps) {
                    try {
                        builder.addAllowedApplication(pkg)
                    } catch (e: Exception) {
                        Log.w(TAG, "приложение $pkg не найдено")
                    }
                }
            }
            "except" -> {
                for (pkg in apps) {
                    try {
                        builder.addDisallowedApplication(pkg)
                    } catch (e: Exception) {
                        Log.w(TAG, "приложение $pkg не найдено")
                    }
                }
                excludeSelf(builder)
            }
            else -> excludeSelf(builder)
        }
    }

    private fun excludeSelf(builder: Builder) {
        try {
            builder.addDisallowedApplication(packageName)
        } catch (e: Exception) {
            Log.w(TAG, "не удалось исключить себя: ${e.message}")
        }
    }

    /**
     * Останавливает туннель, не занимая главный поток.
     *
     * Разделено надвое намеренно. Всё, что видит человек — надпись, цвет
     * кнопки, уведомление, — делается сразу. А вот погасить ядро может занять
     * секунды: оно ждёт свои фоновые задачи, и одна из них в этот момент
     * пытается достучаться до сервера. Именно так это и ломалось: при плохой
     * связи нажатие на кнопку останавливало главный поток целиком, и
     * приложение выглядело зависшим — жёлтый круг, который не реагирует.
     *
     * finalState нужен для несостоявшегося подключения: туннель гасится так же,
     * но на экране должно остаться «ошибка» и её причина. Раньше причина
     * затиралась сразу же, и человек видел просто «выключен» — без объяснения,
     * почему.
     */
    private fun stopTunnel(finalState: String = "stopped", finalDetail: String = "") {
        ticker.removeCallbacks(poll)
        statsJson = "{}"
        report(finalState, finalDetail)
        stopForeground(STOP_FOREGROUND_REMOVE)

        val iface = fd
        fd = null
        // onDestroy вызывает нас повторно после stopSelf — второй раз гасить
        // нечего.
        if (stopping) return
        stopping = true

        Thread {
            try {
                tunnel.stop()
            } catch (e: Exception) {
                Log.w(TAG, "остановка: ${e.message}")
            }
            try {
                iface?.close()
            } catch (e: Exception) {
                // Дескриптор уже отдан в Go и закрыт там — это нормально.
            }
            stopSelf()
        }.start()
    }

    override fun onDestroy() {
        current = null
        stopTunnel()
        super.onDestroy()
    }

    override fun onRevoke() {
        // Систему попросили отдать VPN другому приложению.
        stopTunnel()
        super.onRevoke()
    }

    // ---------- то, что ядро просит сделать у приложения ----------

    /**
     * Помечает сокет как идущий мимо туннеля.
     *
     * Без этого соединение с сервером ушло бы в собственный туннель и
     * зациклилось: система заворачивает внутрь весь трафик, включая наш.
     */
    override fun protect(fd: Long): Boolean = protect(fd.toInt())

    override fun onState(state: String, detail: String) = report(state, detail)

    override fun onLog(line: String) {
        synchronized(log) {
            log.addLast(line)
            while (log.size > 200) log.removeFirst()
        }
        onUpdate?.invoke()
    }

    /**
     * Разрешает имя средствами системы, минуя туннель.
     *
     * Своими силами ядро этого на телефоне не может: файла с настройками DNS,
     * который читают обычные программы, в Android нет.
     */
    override fun resolveLocal(name: String): String = try {
        InetAddress.getAllByName(name).joinToString(",") { it.hostAddress ?: "" }
    } catch (e: Exception) {
        ""
    }

    private fun report(newState: String, newDetail: String) {
        state = newState
        detail = newDetail
        onUpdate?.invoke()
        if (newState != "stopped") {
            notificationManager().notify(NOTIFICATION_ID, notification(describe(newState, newDetail)))
        }
    }

    private fun describe(state: String, detail: String): String {
        val text = when (state) {
            "connected" -> "подключено"
            "connecting" -> "подключение…"
            "error" -> "ошибка"
            else -> state
        }
        return if (detail.isBlank()) text else "$text: $detail"
    }

    private fun notificationManager(): NotificationManager =
        getSystemService(NotificationManager::class.java)

    private fun notification(text: String): Notification {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                CHANNEL, getString(R.string.app_name), NotificationManager.IMPORTANCE_LOW
            )
            notificationManager().createNotificationChannel(channel)
        }
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val stop = PendingIntent.getService(
            this, 1, Intent(this, TunnelService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        return Notification.Builder(this, CHANNEL)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(text)
            .setSmallIcon(R.drawable.ic_notify)
            .setContentIntent(open)
            .setOngoing(true)
            .addAction(
                Notification.Action.Builder(
                    null as android.graphics.drawable.Icon?,
                    getString(R.string.stop),
                    stop,
                ).build()
            )
            .build()
    }
}
