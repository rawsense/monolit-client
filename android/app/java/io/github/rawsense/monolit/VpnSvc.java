package io.github.rawsense.monolit;

import android.content.Intent;
import android.net.VpnService;
import android.os.Build;
import android.os.ParcelFileDescriptor;
import android.util.Log;

import mobile.Mobile;

// VpnSvc — VPN системы: отдаёт весь трафик телефона ядру через дескриптор TUN.
// Сам трафик не трогает: адреса и маршруты задаёт общая логика, пакеты читает ядро
public class VpnSvc extends VpnService {
    @Override
    public void onCreate() {
        super.onCreate();
        Bridge.I.serviceUp(this);
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        return START_NOT_STICKY;
    }

    @Override
    public void onDestroy() {
        Bridge.I.service = null;
        super.onDestroy();
    }

    // onRevoke: VPN отнял пользователь в настройках или другое приложение VPN
    @Override
    public void onRevoke() {
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
        }
        if (Build.VERSION.SDK_INT >= 29) {
            b.setMetered(false); // тариф сети — как у сети под VPN, а не «лимитная» по умолчанию
        }
        ParcelFileDescriptor pfd = b.establish();
        if (pfd == null) {
            return -1;
        }
        return pfd.detachFd();
    }
}
