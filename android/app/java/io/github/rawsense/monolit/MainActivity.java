package io.github.rawsense.monolit;

import android.app.Activity;
import android.content.Intent;
import android.net.VpnService;
import android.os.Bundle;
import android.view.View;
import android.view.WindowInsets;
import android.webkit.JavascriptInterface;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;

import org.json.JSONObject;

import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

import mobile.Mobile;

// MainActivity — окно: тот же интерфейс, что в Windows, во встроенном браузере. Вызовы интерфейса
// уходят в общую логику по имени (мост MonolitNative, см. core/web/native.js)
public class MainActivity extends Activity {
    private static final int ASK_VPN = 1;

    private final ExecutorService calls = Executors.newCachedThreadPool();
    private WebView web;
    private CountDownLatch asked;
    private volatile boolean allowed;

    @Override
    protected void onCreate(Bundle saved) {
        super.onCreate(saved);
        String ui = Bridge.I.start(this);
        web = new WebView(this);
        web.setBackgroundColor(getColor(R.color.bg));
        WebSettings s = web.getSettings();
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true);
        s.setTextZoom(100); // размер шрифта системы не ломает вёрстку
        web.setOverScrollMode(View.OVER_SCROLL_NEVER);
        web.setWebViewClient(new WebViewClient());
        web.addJavascriptInterface(new Native(), "MonolitNative");
        // с Android 15 окно рисуется под строкой состояния и кнопками системы: отступаем на их высоту.
        // Отступ — у рамки вокруг браузера: сам встроенный браузер свои отступы не учитывает
        FrameLayout box = new FrameLayout(this);
        box.setBackgroundColor(getColor(R.color.bg));
        box.addView(web);
        box.setOnApplyWindowInsetsListener((v, in) -> {
            if (android.os.Build.VERSION.SDK_INT >= 30) {
                android.graphics.Insets b = in.getInsets(WindowInsets.Type.systemBars() | WindowInsets.Type.ime());
                v.setPadding(b.left, b.top, b.right, b.bottom);
            } else {
                v.setPadding(in.getSystemWindowInsetLeft(), in.getSystemWindowInsetTop(),
                        in.getSystemWindowInsetRight(), in.getSystemWindowInsetBottom());
            }
            return in;
        });
        setContentView(box);
        Bridge.I.activity = this;
        web.loadUrl(ui);
    }

    @Override
    protected void onDestroy() {
        if (Bridge.I.activity == this) {
            Bridge.I.activity = null;
        }
        web.destroy();
        super.onDestroy();
    }

    @Override
    public void onBackPressed() {
        moveTaskToBack(true); // «назад» сворачивает, VPN и окно остаются
    }

    // update: новое состояние от общей логики — в окно
    void update(String state) {
        runOnUiThread(() -> web.evaluateJavascript("window.mp && mp.update(" + state + ")", null));
    }

    // askVpn: спросить у пользователя разрешение на VPN. Зовётся не из потока окна, ждёт ответа
    boolean askVpn() throws InterruptedException {
        CountDownLatch done = new CountDownLatch(1);
        allowed = false;
        runOnUiThread(() -> {
            Intent i = VpnService.prepare(this);
            if (i == null) {
                allowed = true;
                done.countDown();
                return;
            }
            asked = done;
            startActivityForResult(i, ASK_VPN);
        });
        done.await();
        return allowed;
    }

    @Override
    protected void onActivityResult(int request, int result, Intent data) {
        if (request == ASK_VPN && asked != null) {
            allowed = result == RESULT_OK;
            asked.countDown();
            asked = null;
        }
    }

    // Native — мост из окна: вызов по имени идёт в фоне, ответ возвращается в то же обещание
    private final class Native {
        @JavascriptInterface
        public String names() {
            return Mobile.names();
        }

        @JavascriptInterface
        public void call(int id, String name, String args) {
            calls.execute(() -> {
                String js;
                try {
                    js = "__mpDone(" + id + ",true," + Mobile.call(name, args) + ")";
                } catch (Exception e) {
                    js = "__mpDone(" + id + ",false," + JSONObject.quote(String.valueOf(e.getMessage())) + ")";
                }
                String run = js;
                runOnUiThread(() -> web.evaluateJavascript(run, null));
            });
        }
    }
}
