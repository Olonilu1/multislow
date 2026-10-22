# MultiSlow for Android

A VPN app that routes the whole phone through 3 DNS tunnels at once.

```
phone apps -> VPN (TUN) -> tun2socks -> local SOCKS5 -> Pool -> SSH x3 -> dnstt-client x3 -> DNS -> your 3 servers
phone DNS lookups (UDP) ---------------> DNS-over-TCP through the same pool
```

## Get the APK (no Android Studio needed)

1. Make a free GitHub account and a new private repository.
2. Upload every file in this folder to it (keep the folder structure).
3. Open the repository's **Actions** tab, choose **Build APK**, press **Run workflow**.
4. When it finishes (about 10 minutes), download the `multislow-debug-apk` artifact, unzip it, and install `app-debug.apk` on the phone (allow "install unknown apps" when asked).

If you have a computer with Go, JDK 17, the Android SDK/NDK and gradle, `./build.sh` does the same locally.

## Use it

1. Open the app and edit the config box: for each of the 3 entries set the tunnel domain, server public key (hex), resolver, and the SSH user, password and host key fingerprint (`SHA256:...`).
2. Press **Connect** and accept the Android VPN prompt.
3. The screen shows each tunnel's status, latency, open connections and total served.

Each of the 3 servers needs the standard dnstt server running, an NS record for its tunnel domain, and an SSH account that allows TCP forwarding.

## Known limits

- IPv4 only: IPv6 traffic is not routed through the VPN.
- Only DNS is relayed for UDP. Other UDP (QUIC/HTTP3, voice calls) is dropped, so apps fall back to TCP.
- Not run as a foreground service yet, so aggressive battery savers may stop it. Exclude the app from battery optimisation.
- Debug-signed APK; fine for personal use.
- The Kotlin app, the gomobile wrapper and the CI workflow have never been compiled or run by the author. Expect to fix small build errors on the first run.
