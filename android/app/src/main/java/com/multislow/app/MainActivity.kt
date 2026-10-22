package com.multislow.app

import android.app.Activity
import android.content.Intent
import android.graphics.Typeface
import android.net.VpnService
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.InputType
import android.view.Gravity
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import org.json.JSONArray

/** One screen: edit the config, press Connect, watch the three tunnels. */
class MainActivity : Activity() {

    private lateinit var configBox: EditText
    private lateinit var statusView: TextView
    private lateinit var button: Button
    private val ui = Handler(Looper.getMainLooper())

    private val refresh = object : Runnable {
        override fun run() {
            render()
            ui.postDelayed(this, 1500)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val prefs = getSharedPreferences("multislow", MODE_PRIVATE)

        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(32, 64, 32, 32)
        }

        root.addView(TextView(this).apply {
            text = "MultiSlow — 3 tunnels, one connection"
            textSize = 18f
            setTypeface(typeface, Typeface.BOLD)
        })

        configBox = EditText(this).apply {
            setText(prefs.getString("config", TEMPLATE))
            typeface = Typeface.MONOSPACE
            textSize = 11f
            gravity = Gravity.TOP or Gravity.START
            inputType = InputType.TYPE_CLASS_TEXT or
                InputType.TYPE_TEXT_FLAG_MULTI_LINE or
                InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS or
                InputType.TYPE_TEXT_VARIATION_VISIBLE_PASSWORD
        }
        root.addView(configBox, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, 0, 1f))

        statusView = TextView(this).apply {
            typeface = Typeface.MONOSPACE
            textSize = 12f
            setPadding(0, 16, 0, 16)
        }
        root.addView(statusView)

        button = Button(this).apply {
            setOnClickListener {
                if (TunnelVpnService.running || TunnelVpnService.state.startsWith("Connecting")) {
                    disconnect()
                } else {
                    connect()
                }
            }
        }
        root.addView(button)

        setContentView(root)
    }

    override fun onResume() {
        super.onResume()
        ui.post(refresh)
    }

    override fun onPause() {
        ui.removeCallbacks(refresh)
        saveConfig()
        super.onPause()
    }

    private fun saveConfig() {
        getSharedPreferences("multislow", MODE_PRIVATE).edit()
            .putString("config", configBox.text.toString()).apply()
    }

    private fun connect() {
        saveConfig()
        val consent = VpnService.prepare(this)
        if (consent != null) {
            startActivityForResult(consent, REQ_VPN)
        } else {
            startVpn()
        }
    }

    @Deprecated("Deprecated in Java")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == REQ_VPN) {
            if (resultCode == RESULT_OK) startVpn() else statusView.text = "VPN permission denied"
        }
    }

    private fun startVpn() {
        startService(Intent(this, TunnelVpnService::class.java).setAction(TunnelVpnService.ACTION_START))
    }

    private fun disconnect() {
        startService(Intent(this, TunnelVpnService::class.java).setAction(TunnelVpnService.ACTION_STOP))
    }

    private fun render() {
        val busy = TunnelVpnService.running || TunnelVpnService.state.startsWith("Connecting")
        button.text = if (busy) "Disconnect" else "Connect"

        val sb = StringBuilder(TunnelVpnService.state)
        if (TunnelVpnService.running) {
            try {
                val tunnels = JSONArray(mobile.Mobile.stats())
                for (i in 0 until tunnels.length()) {
                    val t = tunnels.getJSONObject(i)
                    sb.append("\n")
                        .append(t.getString("name"))
                        .append(if (t.getBoolean("down")) "  DOWN" else "  up")
                        .append("  ").append(t.getLong("latency_ms")).append(" ms")
                        .append("  open ").append(t.getLong("active"))
                        .append("  served ").append(t.getLong("served"))
                }
            } catch (_: Throwable) {
            }
        }
        statusView.text = sb
    }

    companion object {
        private const val REQ_VPN = 1

        // Fill in your three servers. "{dnstt}" is replaced with the bundled dnstt-client.
        private const val TEMPLATE = """{
  "listen": "127.0.0.1:1080",
  "strategy": "fastest",
  "entries": [
    {
      "name": "tunnel-1",
      "listen": "127.0.0.1:2201",
      "client_cmd": ["{dnstt}", "-udp", "8.8.8.8:53", "-pubkey", "SERVER1_PUBKEY_HEX", "t1.example.com", "127.0.0.1:2201"],
      "ssh": {"user": "USER1", "password": "PASSWORD1", "host_key_sha256": "SHA256:SERVER1_FINGERPRINT"}
    },
    {
      "name": "tunnel-2",
      "listen": "127.0.0.1:2202",
      "client_cmd": ["{dnstt}", "-udp", "1.1.1.1:53", "-pubkey", "SERVER2_PUBKEY_HEX", "t2.example.org", "127.0.0.1:2202"],
      "ssh": {"user": "USER2", "password": "PASSWORD2", "host_key_sha256": "SHA256:SERVER2_FINGERPRINT"}
    },
    {
      "name": "tunnel-3",
      "listen": "127.0.0.1:2203",
      "client_cmd": ["{dnstt}", "-udp", "9.9.9.9:53", "-pubkey", "SERVER3_PUBKEY_HEX", "t3.example.net", "127.0.0.1:2203"],
      "ssh": {"user": "USER3", "password": "PASSWORD3", "host_key_sha256": "SHA256:SERVER3_FINGERPRINT"}
    }
  ]
}"""
    }
}
