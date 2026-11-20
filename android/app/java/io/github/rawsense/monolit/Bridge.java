package io.github.rawsense.monolit;

import android.content.Context;
import android.content.Intent;
import android.net.VpnService;
import android.os.Build;
import android.util.Log;

import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

import mobile.Host;
import mobile.Mobile;

// Bridge — то, что приложение даёт общей логике (библиотека Go): VPN системы, вывод сокетов из VPN,
// состояние для окна. Один на процесс: окно и сервис живут и умирают отдельно друг от друга
final class Bridge implements Host {
    static final String TAG = "monolit";
    static final Bridge I = new Bridge();

    private Context app;
    private String ui;
    volatile MainActivity activity;
    volatile VpnSvc service;
    private volatile CountDownLatch svcReady = new CountDownLatch(0);

    // start запускает общую логику (один раз на процесс) и отдаёт адрес интерфейса для окна
    synchronized String start(Context c) {
        if (ui == null) {
            app = c.getApplicationContext();
            ui = Mobile.start(app.getFilesDir().getAbsolutePath(), this);
        }
        return ui;
    }

    void serviceUp(VpnSvc s) {
        service = s;
        svcReady.countDown();
    }

    @Override
    public boolean protect(int fd) {
        VpnSvc s = service;
        return s != null && s.protect(fd);
    }

    // openTun зовёт библиотека из своего потока: можно ждать согласия пользователя и запуска сервиса
    @Override
    public int openTun(int mtu, String addr4, int prefix4, String dns4, String addr6, int prefix6) {
        try {
            if (VpnService.prepare(app) != null) { // пользователь ещё не разрешал VPN этому приложению
                MainActivity a = activity;
                if (a == null || !a.askVpn()) {
                    return -1;
                }
            }
            VpnSvc s = service;
            if (s == null) {
                svcReady = new CountDownLatch(1);
                app.startService(new Intent(app, VpnSvc.class));
                if (!svcReady.await(5, TimeUnit.SECONDS)) {
                    Log.e(TAG, "сервис VPN не запустился");
                    return -1;
                }
                s = service;
            }
            return s.open(mtu, addr4, prefix4, dns4, addr6, prefix6);
        } catch (Exception e) {
            Log.e(TAG, "VPN не поднялся", e);
            return -1;
        }
    }

    @Override
    public void closeTun() {
        VpnSvc s = service;
        if (s != null) {
            s.stopSelf();
        }
    }

    @Override
    public void push(String state) {
        MainActivity a = activity;
        if (a != null) {
            a.update(state);
        }
    }

    @Override
    public String osVersion() {
        return Build.VERSION.RELEASE;
    }

    @Override
    public String device() {
        return Build.MANUFACTURER + " " + Build.MODEL;
    }
}
