package io.github.rawsense.monolit;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.net.VpnService;
import android.os.Build;
import android.os.ParcelFileDescriptor;
import android.util.Log;

import mobile.Mobile;

// VpnSvc — VPN системы: отдаёт весь трафик телефона ядру через дескриптор TUN.
// Сам трафик не трогает: адреса и маршруты задаёт общая логика, пакеты читает ядро
public class VpnSvc extends VpnService {
    static final String STOP = "io.github.rawsense.monolit.STOP";
    private static final String CHANNEL = "vpn";
    private static final int NOTE = 1;

    @Override
    public void onCreate() {
        super.onCreate();
        Bridge.I.start(this);
        Bridge.I.serviceUp(this);
        Mobile.log("сервис VPN: создан");
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        String act = intent == null ? null : intent.getAction();
        Mobile.log("сервис VPN: запуск, действие " + act);
        if (STOP.equals(act)) { // «Отключить» в уведомлении
            new Thread(Mobile::disconnect, "stop").start();
        } else if (SERVICE_INTERFACE.equals(act)) {
            // «постоянный VPN»: система запустила сервис сама (после перезагрузки или обрыва), окна нет
            new Thread(Mobile::connect, "always-on").start();
        }
        return START_NOT_STICKY;
    }

    @Override
    public void onDestroy() {
        Bridge.I.service = null;
        Mobile.log("сервис VPN: завершён");
        super.onDestroy();
    }

    // onRevoke: VPN отнял пользователь в настройках или другое приложение VPN
    @Override
    public void onRevoke() {
        Mobile.log("сервис VPN: система отняла VPN (выключен в настройках или включён другой VPN)");
        new Thread(Mobile::disconnect, "revoke").start();
    }

    // open поднимает VPN и отдаёт дескриптор, отсоединённый от Java: дальше им владеет ядро,
    // VPN держится, пока ядро его не закроет
    int open(int mtu, String addr4, int prefix4, String dns4, String addr6, int prefix6) {
        Builder b = new Builder()
                .setSession("Monolit")
                .setMtu(mtu)
                .addAddress(addr4, prefix4)
                .addDnsServer(dns4)
                .addRoute("0.0.0.0", 0);
        try { // IPv6 заводим в туннель, где ядро его отклоняет: иначе он шёл бы мимо VPN
            b.addAddress(addr6, prefix6).addRoute("::", 0);
        } catch (Exception e) {
            Log.w(Bridge.TAG, "IPv6 в VPN не добавлен: " + e);
            Mobile.log("IPv6 в VPN не добавлен: " + e);
        }
        if (Build.VERSION.SDK_INT >= 29) {
            b.setMetered(false); // тариф сети — как у сети под VPN, а не «лимитная» по умолчанию
        }
        ParcelFileDescriptor pfd = b.establish();
        if (pfd == null) {
            return -1;
        }
        foreground();
        return pfd.detachFd();
    }

    // close: VPN снят — убрать уведомление и завершиться
    void close() {
        stopForeground(true);
        stopSelf();
    }

    // refresh: состояние поменялось — обновить текст уведомления
    void refresh() {
        if (Bridge.I.on || !Bridge.I.busy.isEmpty()) {
            getSystemService(NotificationManager.class).notify(NOTE, note());
        }
    }

    // foreground: сервис с уведомлением система не выгружает из памяти, пока VPN включён
    private void foreground() {
        try {
            NotificationManager nm = getSystemService(NotificationManager.class);
            if (Build.VERSION.SDK_INT >= 26 && nm.getNotificationChannel(CHANNEL) == null) {
                NotificationChannel ch = new NotificationChannel(CHANNEL, "Состояние VPN", NotificationManager.IMPORTANCE_LOW);
                ch.setShowBadge(false);
                nm.createNotificationChannel(ch);
            }
            if (Build.VERSION.SDK_INT >= 34) {
                startForeground(NOTE, note(), ServiceInfo.FOREGROUND_SERVICE_TYPE_SYSTEM_EXEMPTED);
            } else {
                startForeground(NOTE, note());
            }
        } catch (Exception e) {
            Log.e(Bridge.TAG, "уведомление не показано", e);
            Mobile.log("уведомление не показано: " + e);
        }
    }

    private Notification note() {
        PendingIntent open = PendingIntent.getActivity(this, 0, new Intent(this, MainActivity.class), PendingIntent.FLAG_IMMUTABLE);
        PendingIntent stop = PendingIntent.getService(this, 1, new Intent(this, VpnSvc.class).setAction(STOP), PendingIntent.FLAG_IMMUTABLE);
        String busy = Bridge.I.busy;
        Notification.Builder b = Build.VERSION.SDK_INT >= 26 ? new Notification.Builder(this, CHANNEL) : new Notification.Builder(this);
        b.setSmallIcon(R.drawable.ic_stat)
                .setContentTitle(busy.isEmpty() ? "Подключено" : busy)
                .setContentText(Bridge.I.conn)
                .setContentIntent(open)
                .setOngoing(true)
                .setShowWhen(false)
                .addAction(new Notification.Action.Builder(null, "Отключить", stop).build());
        return b.build();
    }
}
