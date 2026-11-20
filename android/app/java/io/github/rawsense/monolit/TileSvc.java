package io.github.rawsense.monolit;

import android.app.PendingIntent;
import android.content.Intent;
import android.net.VpnService;
import android.os.Build;
import android.service.quicksettings.Tile;
import android.service.quicksettings.TileService;

import mobile.Mobile;

// TileSvc — плитка в шторке: включить и выключить VPN без окна
public class TileSvc extends TileService {
    @Override
    public void onStartListening() {
        Bridge.I.start(this);
        Tile t = getQsTile();
        if (t == null) {
            return;
        }
        boolean on = Bridge.I.on || !Bridge.I.busy.isEmpty();
        t.setState(on ? Tile.STATE_ACTIVE : Tile.STATE_INACTIVE);
        t.setLabel("Monolit");
        if (Build.VERSION.SDK_INT >= 29) {
            t.setSubtitle(Bridge.I.busy.isEmpty() ? (Bridge.I.on ? Bridge.I.conn : "") : Bridge.I.busy);
        }
        t.updateTile();
    }

    @Override
    public void onClick() {
        Bridge.I.start(this);
        if (!Bridge.I.on && VpnService.prepare(this) != null) {
            // разрешение на VPN ещё не дано: его спрашивает окно
            Intent i = new Intent(this, MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
            if (Build.VERSION.SDK_INT >= 34) {
                startActivityAndCollapse(PendingIntent.getActivity(this, 0, i, PendingIntent.FLAG_IMMUTABLE));
            } else {
                startActivityAndCollapse(i);
            }
            return;
        }
        new Thread(Mobile::toggle, "tile").start();
    }
}
