package com.subclerk.app

import android.app.Application
import android.content.Context

class SubclerkApp : Application() {
    lateinit var api: SubclerkApi
        private set

    override fun onCreate() {
        super.onCreate()
        instance = this
        val prefs = getSharedPreferences("subclerk", Context.MODE_PRIVATE)
        val server = prefs.getString("server", "") ?: ""
        api = SubclerkApi(server)
    }

    fun updateServer(server: String) {
        getSharedPreferences("subclerk", Context.MODE_PRIVATE)
            .edit().putString("server", server).apply()
        api = SubclerkApi(server)
        // Re-register playback service with new server
        PlaybackService.instance?.registerAndConnect()
    }

    companion object {
        lateinit var instance: SubclerkApp
            private set
    }
}
