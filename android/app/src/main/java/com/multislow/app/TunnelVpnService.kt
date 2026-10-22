package com.multislow.app

import android.content.Intent
import android.net.VpnService
import android.os.ParcelFileDescriptor
import java.io.File
import kotlin.concurrent.thread

/**
 * Owns the VPN interface. Starting it creates the TUN device and hands it to
 * the Go library, which runs the tunnels, the pool and the packet engine.
 */
class TunnelVpnService : VpnService() {

    companion object {
        const val ACTION_START = "com.multislow.app.START"
        const val ACTION_STOP = "com.multislow.app.STOP"

        @Volatile var running = false
        @Volatile var state = "Disconnected"
    }

    private var tun: ParcelFileDescriptor? = null
    private var worker: Thread? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_START -> start()
            ACTION_STOP -> {
                stop()
                stopSelf()
            }
        }
        return START_NOT_STICKY
    }

    private fun start() {
        if (running || worker?.isAlive == true) return
        state = "Connecting…"
        val config = getSharedPreferences("multislow", MODE_PRIVATE).getString("config", "") ?: ""

        // Starting blocks on the first health probe, so keep it off the main thread.
        worker = thread(name = "multislow-start") {
            try {
                val pfd = Builder()
                    .setSession("MultiSlow")
                    .setMtu(1500)
                    .addAddress("10.77.0.2", 32)
                    .addRoute("0.0.0.0", 0)
                    .addDnsServer("1.1.1.1")
                    // Keep this app's own traffic (the DNS tunnel clients) off the VPN,
                    // otherwise their packets would loop back into it.
                    .addDisallowedApplication(packageName)
                    .establish() ?: throw IllegalStateException("VPN permission was not granted")
                tun = pfd

                val dnstt = File(applicationInfo.nativeLibraryDir, "libdnstt.so").absolutePath
                val fd = pfd.detachFd() // the Go side now owns the descriptor
                tun = null
                mobile.Mobile.start(config, fd.toLong(), dnstt)

                running = true
                state = "Connected"
            } catch (e: Throwable) {
                state = "Error: ${e.message}"
                stop()
                stopSelf()
            }
        }
    }

    private fun stop() {
        try {
            mobile.Mobile.stop()
        } catch (_: Throwable) {
        }
        try {
            tun?.close()
        } catch (_: Exception) {
        }
        tun = null
        running = false
        if (!state.startsWith("Error")) state = "Disconnected"
    }

    override fun onRevoke() {
        stop()
        super.onRevoke()
    }

    override fun onDestroy() {
        stop()
        super.onDestroy()
    }
}
