package com.subclerk.app

import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.media3.common.MediaItem
import androidx.media3.common.Player
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.session.MediaSession
import androidx.media3.session.MediaSessionService
import android.os.Handler
import android.os.Looper
import kotlinx.coroutines.*
import okhttp3.OkHttpClient
import okhttp3.Request
import org.json.JSONObject
import java.io.BufferedReader
import java.io.InputStreamReader
import java.util.concurrent.TimeUnit

class PlaybackService : MediaSessionService() {
    private var mediaSession: MediaSession? = null
    private var player: ExoPlayer? = null
    private var sseJob: Job? = null
    private var heartbeatJob: Job? = null
    var deviceId: String? = null
        private set
    private val scope = CoroutineScope(Dispatchers.IO + SupervisorJob())
    private val mainHandler = Handler(Looper.getMainLooper())

    // ReplayGain: gain values per playlist index
    private val replayGainMap = mutableMapOf<Int, Pair<Double, Double>>() // index -> (trackGain, albumGain)
    // Track which playlist indices are playing from offline cache
    private val offlineIndexes = mutableSetOf<Int>()

    override fun onCreate() {
        super.onCreate()
        instance = this

        // Create notification channel and start as foreground service
        // so we stay alive for device discovery even when not playing
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

        val exo = ExoPlayer.Builder(this).build()
        player = exo
        mediaSession = MediaSession.Builder(this, exo).build()

        exo.addListener(object : Player.Listener {
            override fun onPlaybackStateChanged(state: Int) {
                if (state == Player.STATE_ENDED) {
                    // Auto-advance handled by master queue
                }
            }

            override fun onMediaItemTransition(mediaItem: MediaItem?, reason: Int) {
                applyReplayGain()
            }
        })

        registerAndConnect()
    }

    private fun applyReplayGain() {
        val p = player ?: return
        val prefs = getSharedPreferences("subclerk", MODE_PRIVATE)
        val mode = prefs.getString("replaygain", "off") ?: "off"
        if (mode == "off") {
            p.volume = 1.0f
            return
        }
        val idx = p.currentMediaItemIndex
        val gains = replayGainMap[idx]
        if (gains == null) {
            p.volume = 1.0f
            return
        }
        val gain = if (mode == "album") gains.second else gains.first
        // Convert dB gain to linear volume: 10^(gain/20)
        val linear = Math.pow(10.0, gain / 20.0).toFloat().coerceIn(0f, 4f)
        android.util.Log.d("PlaybackService", "ReplayGain: mode=$mode idx=$idx gain=${gain}dB volume=$linear")
        p.volume = linear
    }

    fun registerAndConnect() {
        val api = SubclerkApp.instance.api
        if (!api.isConfigured) return

        // Cancel previous connections
        sseJob?.cancel()
        heartbeatJob?.cancel()

        scope.launch {
            val prefs = getSharedPreferences("subclerk", MODE_PRIVATE)
            val name = prefs.getString("device_name", null) ?: "android-${android.os.Build.MODEL}".replace(" ", "-").lowercase()
            val format = prefs.getString("audio_format", "") ?: ""
            val bitrate = prefs.getInt("audio_bitrate", 0)
            // Only pass navidrome_url when on external network
            val navidromeUrl = if (SubclerkApp.instance.isOnHomeWifi()) "" else (prefs.getString("navidrome_url", "") ?: "")
            val replaygain = prefs.getString("replaygain", "off") ?: "off"

            val id = api.registerDevice(name, format, bitrate, navidromeUrl, replaygain)
            if (id.isNullOrBlank()) {
                android.util.Log.e("PlaybackService", "Failed to register device")
                return@launch
            }
            android.util.Log.d("PlaybackService", "Registered as device: $id")
            deviceId = id

            // Start SSE listener so we receive commands when this device becomes active
            sseJob = scope.launch { listenSSE(id) }

            // Report playback status every second so handoff captures accurate position
            heartbeatJob = scope.launch {
                var heartbeatCounter = 0
                while (isActive) {
                    delay(1_000)
                    // Read player state on main thread (ExoPlayer requirement)
                    val snapshot = kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.Main) {
                        val p = player ?: return@withContext null
                        Triple(!p.isPlaying, p.currentPosition / 1000.0, p.duration.let { if (it > 0) it / 1000.0 else 0.0 }) to p.currentMediaItemIndex
                    } ?: continue
                    val (state, playlistPos) = snapshot
                    val (paused, timePos, duration) = state
                    api.reportStatus(
                        id = id,
                        pause = paused,
                        timePos = timePos,
                        duration = duration,
                        playlistPos = playlistPos
                    )
                    // Heartbeat every 10s
                    heartbeatCounter++
                    if (heartbeatCounter >= 10) {
                        heartbeatCounter = 0
                        api.heartbeat(id)
                    }
                }
            }
        }
    }

    private fun scheduleStatusReport() {
        val id = deviceId ?: return
        // Delay slightly so ExoPlayer resolves duration
        mainHandler.postDelayed({
            val p = player ?: return@postDelayed
            val paused = !p.isPlaying
            val timePos = p.currentPosition / 1000.0
            val duration = p.duration.let { if (it > 0) it / 1000.0 else 0.0 }
            val playlistPos = p.currentMediaItemIndex
            scope.launch {
                SubclerkApp.instance.api.reportStatus(id, paused, timePos, duration, playlistPos)
            }
        }, 500)
    }

    val isCurrentTrackOffline: Boolean
        get() = player?.let { offlineIndexes.contains(it.currentMediaItemIndex) } ?: false

    companion object {
        var instance: PlaybackService? = null
            private set
    }

    private suspend fun listenSSE(id: String) {
        val api = SubclerkApp.instance.api
        val sseClient = OkHttpClient.Builder()
            .readTimeout(0, TimeUnit.SECONDS) // infinite for SSE
            .build()

        while (true) {
            try {
                val url = api.sseUrl(id)
                android.util.Log.d("PlaybackService", "SSE connecting to: $url")
                val req = Request.Builder().url(url).build()
                sseClient.newCall(req).execute().use { resp ->
                    if (!resp.isSuccessful) {
                        android.util.Log.e("PlaybackService", "SSE connection failed: ${resp.code}")
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
                                mainHandler.post { handleCommand(cmd) }
                            } catch (e: Exception) {
                                android.util.Log.e("PlaybackService", "SSE parse error: ${e.message}")
                            }
                        }
                    }
                    android.util.Log.d("PlaybackService", "SSE stream ended")
                }
            } catch (e: Exception) {
                android.util.Log.e("PlaybackService", "SSE error: ${e.message}")
                delay(5000) // reconnect after error
            }
        }
    }

    private fun handleCommand(cmd: JSONObject) {
        val p = player ?: return
        val action = cmd.optString("action")
        val data = cmd.optJSONObject("data")

        when (action) {
            "load" -> {
                val url = data?.optString("url") ?: return
                val mode = data.optString("mode", "replace")
                val songId = data.optString("song_id", "")
                android.util.Log.d("PlaybackService", "load: mode=$mode songId=$songId url=${url.take(80)}...")
                // Use offline file if available
                val offline = SubclerkApp.instance.offlineManager
                val localPath = if (songId.isNotBlank()) offline.getLocalPath(songId) else null
                val mediaUri = localPath ?: url
                val item = MediaItem.fromUri(mediaUri)
                // Extract ReplayGain data
                val rg = data.optJSONObject("replay_gain")
                val trackGain = rg?.optDouble("track_gain", 0.0) ?: 0.0
                val albumGain = rg?.optDouble("album_gain", 0.0) ?: 0.0
                when (mode) {
                    "replace" -> {
                        replayGainMap.clear()
                        offlineIndexes.clear()
                        replayGainMap[0] = Pair(trackGain, albumGain)
                        if (localPath != null) offlineIndexes.add(0)
                        p.setMediaItem(item)
                        p.prepare()
                        p.play()
                        applyReplayGain()
                        scheduleStatusReport()
                    }
                    "append", "append-play" -> {
                        val idx = p.mediaItemCount
                        replayGainMap[idx] = Pair(trackGain, albumGain)
                        if (localPath != null) offlineIndexes.add(idx)
                        p.addMediaItem(item)
                        if (p.mediaItemCount == 1 || mode == "append-play") {
                            p.seekTo(p.mediaItemCount - 1, 0)
                            p.prepare()
                            p.play()
                            applyReplayGain()
                            scheduleStatusReport()
                        }
                    }
                }
            }
            "play" -> { p.prepare(); p.play(); scheduleStatusReport() }
            "pause" -> p.pause()
            "stop" -> { p.pause(); p.seekTo(0) }
            "seek" -> {
                val pos = data?.optDouble("position", -1.0) ?: -1.0
                if (pos >= 0) p.seekTo((pos * 1000).toLong())
            }
            "next" -> {
                if (p.hasNextMediaItem()) p.seekToNextMediaItem()
            }
            "prev" -> {
                if (p.hasPreviousMediaItem()) p.seekToPreviousMediaItem()
            }
            "clear" -> {
                replayGainMap.clear()
                offlineIndexes.clear()
                p.clearMediaItems()
                p.stop()
            }
            "playlist-remove" -> {
                val idx = data?.optInt("index", -1) ?: -1
                if (idx in 0 until p.mediaItemCount) p.removeMediaItem(idx)
            }
            "playlist-move" -> {
                val from = data?.optInt("from", -1) ?: -1
                val to = data?.optInt("to", -1) ?: -1
                if (from in 0 until p.mediaItemCount && to in 0..p.mediaItemCount) {
                    p.moveMediaItem(from, to)
                }
            }
            "handoff" -> {
                // Atomic handoff: seek to correct track + position after playlist is loaded
                val pos = data?.optInt("playlist_pos", 0) ?: 0
                val timePos = data?.optDouble("time_pos", 0.0) ?: 0.0
                val paused = data?.optBoolean("paused", false) ?: false
                android.util.Log.d("PlaybackService", "handoff: pos=$pos timePos=$timePos paused=$paused items=${p.mediaItemCount}")
                if (pos in 0 until p.mediaItemCount) {
                    p.seekTo(pos, (timePos * 1000).toLong())
                    p.prepare()
                    if (!paused) p.play() else p.pause()
                }
                scheduleStatusReport()
            }
            "set-property" -> {
                val name = data?.optString("name") ?: return
                if (name == "playlist-pos") {
                    val pos = data.optInt("value", -1)
                    if (pos in 0 until p.mediaItemCount) {
                        p.seekTo(pos, 0)
                        p.prepare()
                        p.play()
                    }
                }
            }
        }
    }

    override fun onGetSession(controllerInfo: MediaSession.ControllerInfo): MediaSession? = mediaSession

    override fun onTaskRemoved(rootIntent: Intent?) {
        // Keep running for device discovery and playback
    }

    override fun onDestroy() {
        sseJob?.cancel()
        heartbeatJob?.cancel()
        scope.cancel()
        mediaSession?.run {
            player.release()
            release()
        }
        mediaSession = null
        player = null
        super.onDestroy()
    }
}
