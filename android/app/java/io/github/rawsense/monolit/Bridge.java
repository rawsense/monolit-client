package io.github.rawsense.monolit;

import android.content.ComponentName;
import android.content.Context;
import android.content.Intent;
import android.net.ConnectivityManager;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.net.NetworkRequest;
import android.net.VpnService;
import android.os.Build;
import android.service.quicksettings.TileService;
import android.util.Log;

import org.json.JSONObject;

import java.io.File;
import java.io.FileWriter;
import java.io.PrintWriter;
import java.util.HashMap;
import java.util.Map;

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
    // последнее состояние общей логики: для уведомления и плитки
    volatile boolean on;
    volatile String busy = "";
    volatile String conn = "";
    private volatile CountDownLatch svcReady = new CountDownLatch(0);

    // start запускает общую логику (один раз на процесс) и отдаёт адрес интерфейса для окна
    synchronized String start(Context c) {
        if (ui == null) {
            app = c.getApplicationContext();
            // журнал и аварии — в папке приложения на общем накопителе: её видно по USB (android/logs.ps1)
            File logs = app.getExternalFilesDir(null);
            if (logs == null) {
                logs = app.getFilesDir();
            }
            catchCrashes(new File(logs, "crash.log"));
            ui = Mobile.start(app.getFilesDir().getAbsolutePath(), logs.getAbsolutePath(), this);
            watchNetworks();
        }
        return ui;
    }

    // catchCrashes: авария в коде приложения (Java) — в crash.log и журнал, затем обычное падение
    private void catchCrashes(File file) {
        Thread.UncaughtExceptionHandler old = Thread.getDefaultUncaughtExceptionHandler();
        Thread.setDefaultUncaughtExceptionHandler((t, e) -> {
            try (PrintWriter w = new PrintWriter(new FileWriter(file, true))) {
                w.println("=== авария Java в потоке " + t.getName() + " ===");
                e.printStackTrace(w);
            } catch (Exception ignored) {
            }
            try {
                Mobile.log("авария Java в потоке " + t.getName() + ": " + e);
            } catch (Throwable ignored) {
            }
            if (old != null) {
                old.uncaughtException(t, e);
            }
        });
    }

    // watchNetworks пишет в журнал смену сетей под VPN (Wi-Fi, мобильная): по ней разбирают обрывы и замирания
    private void watchNetworks() {
        ConnectivityManager cm = app.getSystemService(ConnectivityManager.class);
        NetworkRequest req = new NetworkRequest.Builder()
                .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
                .build();
        Map<Network, String> seen = new HashMap<>();
        try {
            cm.registerNetworkCallback(req, new ConnectivityManager.NetworkCallback() {
                @Override
                public void onCapabilitiesChanged(Network n, NetworkCapabilities c) {
                    String kind = c.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) ? "Wi-Fi"
                            : c.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) ? "мобильная"
                            : c.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) ? "кабель" : "другая";
                    String now = kind + (c.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED) ? "" : ", интернет не подтверждён");
                    synchronized (seen) { // свойства сети приходят часто: пишем только перемену
                        if (now.equals(seen.put(n, now))) {
                            return;
                        }
                    }
                    Mobile.log("сеть " + n + ": " + now);
                }

                @Override
                public void onLost(Network n) {
                    String was;
                    synchronized (seen) {
                        was = seen.remove(n);
                    }
                    Mobile.log("сеть " + n + " пропала" + (was == null ? "" : " (" + was + ")"));
                }
            });
        } catch (Exception e) {
            Mobile.log("слежение за сетью не включилось: " + e);
        }
    }

    void serviceUp(VpnSvc s) {
        service = s;
        svcReady.countDown();
    }

    @Override
    public boolean protect(int fd) {
        VpnSvc s = service;
        return s == null || s.protect(fd); // VPN нет — выводить сокет не из чего (проверка серверов без VPN)
    }

    // openTun зовёт библиотека из своего потока: можно ждать согласия пользователя и запуска сервиса
    @Override
    public int openTun(int mtu, String addr4, int prefix4, String dns4, String addr6, int prefix6) {
        try {
            if (VpnService.prepare(app) != null) { // пользователь ещё не разрешал VPN этому приложению
                MainActivity a = activity;
                if (a == null || !a.askVpn()) {
                    Mobile.log("VPN: нет разрешения пользователя" + (a == null ? ", окно закрыто — спросить некому" : ""));
                    return -1;
                }
            }
            VpnSvc s = service;
            if (s == null) {
                svcReady = new CountDownLatch(1);
                app.startService(new Intent(app, VpnSvc.class));
                if (!svcReady.await(5, TimeUnit.SECONDS)) {
                    Log.e(TAG, "сервис VPN не запустился");
                    Mobile.log("VPN: сервис не запустился за 5 с");
                    return -1;
                }
                s = service;
            }
            return s.open(mtu, addr4, prefix4, dns4, addr6, prefix6);
        } catch (Exception e) {
            Log.e(TAG, "VPN не поднялся", e);
            Mobile.log("VPN не поднялся: " + e);
            return -1;
        }
    }

    @Override
    public void closeTun() {
        VpnSvc s = service;
        if (s != null) {
            s.close();
        }
    }

    @Override
    public void push(String state) {
        MainActivity a = activity;
        if (a != null) {
            a.update(state);
        }
        try {
            JSONObject o = new JSONObject(state);
            boolean nowOn = o.optBoolean("on");
            String nowBusy = o.optString("busy"), nowConn = o.optString("conn");
            if (nowOn == on && nowBusy.equals(busy) && nowConn.equals(conn)) {
                return; // состояние приходит каждую секунду: уведомление и плитку трогаем только при перемене
            }
            on = nowOn;
            busy = nowBusy;
            conn = nowConn;
        } catch (Exception e) {
            return;
        }
        VpnSvc s = service;
        if (s != null) {
            s.refresh();
        }
        TileService.requestListeningState(app, new ComponentName(app, TileSvc.class));
    }

    @Override
    public String timeZone() {
        return java.util.TimeZone.getDefault().getID();
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
