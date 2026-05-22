package com.subclerk.app

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import dev.jdtech.mpv.MPVLib
import kotlinx.coroutines.*
import okhttp3.OkHttpClient
import okhttp3.Request
import org.json.JSONObject
import java.io.BufferedReader
import java.io.InputStreamReader
import java.util.concurrent.TimeUnit

class PlaybackService : Service(), MPVLib.EventObserver {
    private var mpv: MPVLib? = null
    private var sseJob: Job? = null
    private var heartbeatJob: Job? = null
    var deviceId: String? = null
        private set
    private val scope = CoroutineScope(Dispatchers.IO + SupervisorJob())

    // Track which playlist indices are playing from offline cache
    private val offlineIndexes = mutableSetOf<Int>()
    // Pending seek after file load (handoff)
    @Volatile private var pendingSeek: Double? = null
    @Volatile private var pendingPause: Boolean? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        instance = this

        val channelId = "subclerk_service"
        val nm = getSystemService(NotificationManager::class.java)
        if (nm.getNotificationChannel(channelId) == null) {
            nm.createNotificationChannel(
                NotificationChannel(channelId, "Subclerk", NotificationManager.IMPORTANCE_LOW).apply {
                    description = "Keeps Subclerk connected for device discovery"
                }
            )
        }
        val notification = NotificationCompat.Builder(this, channelId)
            .setContentTitle("Subclerk")
            .setContentText("Connected")
            .setSmallIcon(R.drawable.ic_launcher_foreground)
            .setOngoing(true)
            .build()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(1, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PLAYBACK)
        } else {
            startForeground(1, notification)
        }

        initMpv()
        registerAndConnect()
    }

    private fun initMpv() {
        val m = MPVLib.create(applicationContext) ?: run {
            android.util.Log.e("PlaybackService", "Failed to create mpv instance")
            return
        }
        // Audio-only, no video
        m.setOptionString("vid", "no")
        m.setOptionString("vo", "null")
        m.setOptionString("ao", "audiotrack")
        m.setOptionString("idle", "yes")
        m.setOptionString("cache", "yes")
        m.setOptionString("demuxer-max-bytes", "50MiB")
        m.setOptionString("demuxer-max-back-bytes", "25MiB")

        m.init()
        m.addObserver(this)
        mpv = m
        android.util.Log.d("PlaybackService", "mpv initialized")
    }

    // MPVLib.EventObserver
    override fun eventProperty(property: String) {}
    override fun eventProperty(property: String, value: Long) {}
    override fun eventProperty(property: String, value: Double) {}
    override fun eventProperty(property: String, value: Boolean) {}
    override fun eventProperty(property: String, value: String) {}
    override fun event(eventId: Int) {
        // eventId 8 = FILE_LOADED
        if (eventId == 8) {
            applyPendingSeek("event")
        }
    }

    private fun applyPendingSeek(source: String) {
        val seek = pendingSeek
        val pause = pendingPause
        if (seek == null && pause == null) return
        pendingSeek = null
        pendingPause = null
        if (seek != null && seek > 0) {
            android.util.Log.d("PlaybackService", "applying pending seek ($source): $seek")
            mpv?.command(arrayOf("seek", seek.toString(), "absolute"))
        }
        if (pause != null) {
            mpv?.setPropertyBoolean("pause", pause)
        }
    }

    fun registerAndConnect() {
        val api = SubclerkApp.instance.api
        if (!api.isConfigured) return

        sseJob?.cancel()
        heartbeatJob?.cancel()

        scope.launch {
            val prefs = getSharedPreferences("subclerk", MODE_PRIVATE)
            val name = prefs.getString("device_name", null) ?: "android-${android.os.Build.MODEL}".replace(" ", "-").lowercase()
            val format = prefs.getString("audio_format", "") ?: ""
            val bitrate = prefs.getInt("audio_bitrate", 0)
            val navidromeUrl = if (SubclerkApp.instance.isOnHomeWifi()) "" else (prefs.getString("navidrome_url", "") ?: "")
            val replaygain = prefs.getString("replaygain", "off") ?: "off"
            api.deviceSecret = prefs.getString("device_secret", "") ?: ""

            val id = api.registerDevice(name, format, bitrate, navidromeUrl, replaygain)
            if (id.isNullOrBlank()) {
                android.util.Log.e("PlaybackService", "Failed to register device")
                return@launch
            }
            android.util.Log.d("PlaybackService", "Registered as device: $id")
            deviceId = id

            sseJob = scope.launch { listenSSE(id) }

            heartbeatJob = scope.launch {
                var heartbeatCounter = 0
                while (isActive) {
                    delay(1_000)
                    val m = mpv ?: continue
                    val paused = m.getPropertyBoolean("pause") ?: true
                    val timePos = m.getPropertyDouble("time-pos") ?: 0.0
                    val duration = m.getPropertyDouble("duration") ?: 0.0
                    val playlistPos = m.getPropertyInt("playlist-pos")?.toInt() ?: 0
                    api.reportStatus(
                        id = id,
                        pause = paused,
                        timePos = timePos,
                        duration = duration,
                        playlistPos = playlistPos
                    )
                    heartbeatCounter++
                    if (heartbeatCounter >= 10) {
                        heartbeatCounter = 0
                        val ok = api.heartbeat(id)
                        if (!ok) {
                            android.util.Log.w("PlaybackService", "Heartbeat failed, re-registering device")
                            registerAndConnect()
                            return@launch
                        }
                    }
                }
            }
        }
    }

    val isCurrentTrackOffline: Boolean
        get() {
            val pos = mpv?.getPropertyInt("playlist-pos")?.toInt() ?: return false
            return offlineIndexes.contains(pos)
        }

    companion object {
        var instance: PlaybackService? = null
            private set
    }

    private suspend fun listenSSE(id: String) {
        val api = SubclerkApp.instance.api
        val sseClient = OkHttpClient.Builder()
            .readTimeout(0, TimeUnit.SECONDS)
            .build()

        while (true) {
            try {
                val url = api.sseUrl(id)
                android.util.Log.d("PlaybackService", "SSE connecting to: $url")
                val req = Request.Builder().url(url).build()
                sseClient.newCall(req).execute().use { resp ->
                    if (!resp.isSuccessful) {
                        android.util.Log.e("PlaybackService", "SSE connection failed: ${resp.code}")
                        if (resp.code == 404) {
                            android.util.Log.w("PlaybackService", "Device gone from server, re-registering")
                            registerAndConnect()
                            return
                        }
                        return@use
                    }
                    android.util.Log.d("PlaybackService", "SSE connected")
                    val reader = BufferedReader(InputStreamReader(resp.body!!.byteStream()))
                    var line: String?
                    while (reader.readLine().also { line = it } != null) {
                        val l = line ?: continue
                        if (l.startsWith("data: ")) {
                            val data = l.removePrefix("data: ")
                            try {
                                val cmd = JSONObject(data)
                                android.util.Log.d("PlaybackService", "SSE cmd: ${cmd.optString("action")}")
                                handleCommand(cmd)
                            } catch (e: Exception) {
                                android.util.Log.e("PlaybackService", "SSE parse error: ${e.message}")
                            }
                        }
                    }
                    android.util.Log.d("PlaybackService", "SSE stream ended")
                }
            } catch (e: Exception) {
                android.util.Log.e("PlaybackService", "SSE error: ${e.message}")
            }
            delay(5000)
        }
    }

    private fun resolveUrl(url: String, songId: String): String {
        if (songId.isNotBlank()) {
            val localPath = SubclerkApp.instance.offlineManager.getLocalPath(songId)
            if (localPath != null) return localPath
        }
        return url
    }

    private fun handleCommand(cmd: JSONObject) {
        val m = mpv ?: return
        val action = cmd.optString("action")
        val data = cmd.optJSONObject("data")

        when (action) {
            "load" -> {
                val url = data?.optString("url") ?: return
                val mode = data.optString("mode", "replace")
                val songId = data.optString("song_id", "")
                val autoplay = data.optBoolean("autoplay", true)
                val resolvedUrl = resolveUrl(url, songId)
                val isOffline = resolvedUrl != url

                android.util.Log.d("PlaybackService", "load: mode=$mode songId=$songId autoplay=$autoplay url=${url.take(80)}...")

                when (mode) {
                    "replace" -> {
                        offlineIndexes.clear()
                        if (isOffline) offlineIndexes.add(0)
                        m.command(arrayOf("loadfile", resolvedUrl, "replace"))
                        if (!autoplay) {
                            m.setPropertyBoolean("pause", true)
                        }
                        applyReplayGain(data)
                    }
                    "append", "append-play" -> {
                        val idx = (m.getPropertyInt("playlist-count") ?: 0).toInt()
                        if (isOffline) offlineIndexes.add(idx)
                        if (mode == "append-play") {
                            m.command(arrayOf("loadfile", resolvedUrl, "append-play"))
                        } else {
                            m.command(arrayOf("loadfile", resolvedUrl, "append"))
                        }
                    }
                }
            }
            "play" -> m.setPropertyBoolean("pause", false)
            "pause" -> m.setPropertyBoolean("pause", true)
            "stop" -> {
                m.setPropertyBoolean("pause", true)
                m.setPropertyDouble("time-pos", 0.0)
            }
            "seek" -> {
                val pos = data?.optDouble("position", -1.0) ?: -1.0
                if (pos >= 0) m.setPropertyDouble("time-pos", pos)
            }
            "next" -> m.command(arrayOf("playlist-next"))
            "prev" -> m.command(arrayOf("playlist-prev"))
            "clear" -> {
                offlineIndexes.clear()
                m.command(arrayOf("stop"))
                m.command(arrayOf("playlist-clear"))
            }
            "playlist-remove" -> {
                val idx = data?.optInt("index", -1) ?: -1
                if (idx >= 0) m.command(arrayOf("playlist-remove", idx.toString()))
            }
            "playlist-move" -> {
                val from = data?.optInt("from", -1) ?: -1
                val to = data?.optInt("to", -1) ?: -1
                if (from >= 0 && to >= 0) m.command(arrayOf("playlist-move", from.toString(), to.toString()))
            }
            "handoff" -> {
                val pos = data?.optInt("playlist_pos", 0) ?: 0
                val timePos = data?.optDouble("time_pos", 0.0) ?: 0.0
                val paused = data?.optBoolean("paused", false) ?: false
                val tracksArr = data?.optJSONArray("tracks")

                pendingSeek = timePos
                pendingPause = paused

                if (tracksArr != null && tracksArr.length() > 0) {
                    offlineIndexes.clear()
                    m.command(arrayOf("stop"))
                    m.command(arrayOf("playlist-clear"))
                    for (i in 0 until tracksArr.length()) {
                        val t = tracksArr.getJSONObject(i)
                        val url = t.optString("url")
                        val songId = t.optString("song_id", "")
                        val resolvedUrl = resolveUrl(url, songId)
                        if (resolvedUrl != url) offlineIndexes.add(i)
                        m.command(arrayOf("loadfile", resolvedUrl, "append"))
                    }
                }

                android.util.Log.d("PlaybackService", "handoff: pos=$pos timePos=$timePos paused=$paused")
                m.setPropertyInt("playlist-pos", pos)
                applyReplayGainForCurrentTrack()

                // Fallback: if FILE_LOADED already fired (mpv auto-loaded pos 0),
                // the event won't fire again. Try applying seek after a brief yield.
                scope.launch {
                    delay(200)
                    applyPendingSeek("fallback")
                }
            }
            "set-property" -> {
                val name = data?.optString("name") ?: return
                if (name == "playlist-pos") {
                    val pos = data.optInt("value", -1)
                    if (pos >= 0) {
                        m.setPropertyInt("playlist-pos", pos)
                        m.setPropertyBoolean("pause", false)
                    }
                }
            }
        }
    }

    private fun applyReplayGain(trackData: JSONObject?) {
        val m = mpv ?: return
        val prefs = getSharedPreferences("subclerk", MODE_PRIVATE)
        val mode = prefs.getString("replaygain", "off") ?: "off"
        if (mode == "off") {
            m.setPropertyDouble("volume", 100.0)
            return
        }
        val rg = trackData?.optJSONObject("replay_gain")
        if (rg == null) {
            m.setPropertyDouble("volume", 100.0)
            return
        }
        val gain = if (mode == "album") rg.optDouble("album_gain", 0.0) else rg.optDouble("track_gain", 0.0)
        val linear = Math.pow(10.0, gain / 20.0).coerceIn(0.0, 4.0)
        m.setPropertyDouble("volume", linear * 100.0)
        android.util.Log.d("PlaybackService", "ReplayGain: mode=$mode gain=${gain}dB volume=${linear * 100}")
    }

    private fun applyReplayGainForCurrentTrack() {
        val prefs = getSharedPreferences("subclerk", MODE_PRIVATE)
        val mode = prefs.getString("replaygain", "off") ?: "off"
        if (mode == "off") {
            mpv?.setPropertyDouble("volume", 100.0)
        }
        // ReplayGain metadata not available in handoff context, reset to default
        mpv?.setPropertyDouble("volume", 100.0)
    }

    override fun onTaskRemoved(rootIntent: Intent?) {
        // Keep running for device discovery and playback
    }

    override fun onDestroy() {
        sseJob?.cancel()
        heartbeatJob?.cancel()
        scope.cancel()
        mpv?.let {
            it.removeObserver(this)
            it.destroy()
        }
        mpv = null
        instance = null
        super.onDestroy()
    }
}
