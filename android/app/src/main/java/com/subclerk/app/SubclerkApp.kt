package com.subclerk.app

import android.app.Application
import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.wifi.WifiInfo
import android.os.Build

class SubclerkApp : Application() {
    lateinit var api: SubclerkApi
        private set
    lateinit var offlineManager: OfflineManager
        private set

    private var networkCallback: ConnectivityManager.NetworkCallback? = null

    override fun onCreate() {
        super.onCreate()
        instance = this
        offlineManager = OfflineManager(this)
        applyServerForCurrentNetwork()
        startNetworkMonitor()
    }

    fun applyServerForCurrentNetwork() {
        val prefs = getSharedPreferences("subclerk", Context.MODE_PRIVATE)
        val secret = prefs.getString("device_secret", "") ?: ""
        val onHome = isOnHomeWifi()
        val server = if (onHome) {
            prefs.getString("server", "") ?: ""
        } else {
            val ext = prefs.getString("external_server", "") ?: ""
            ext.ifBlank { prefs.getString("server", "") ?: "" }
        }
        android.util.Log.d("SubclerkApp", "Network: onHome=$onHome server=$server ssid=${getCurrentSSID()}")
        if (::api.isInitialized) {
            val oldBase = api.baseUrl
            val newApi = SubclerkApi(server).apply { deviceSecret = secret }
            if (oldBase != newApi.baseUrl) {
                android.util.Log.d("SubclerkApp", "Switching API: $oldBase -> ${newApi.baseUrl}")
                api = newApi
                PlaybackService.instance?.registerAndConnect()
            }
        } else {
            api = SubclerkApi(server).apply { deviceSecret = secret }
            android.util.Log.d("SubclerkApp", "Initial API: ${api.baseUrl}")
        }
    }

    fun isOnHomeWifi(): Boolean {
        val prefs = getSharedPreferences("subclerk", Context.MODE_PRIVATE)
        val homeSSID = prefs.getString("home_wifi_ssid", "") ?: ""
        if (homeSSID.isBlank()) return true // no home SSID configured, assume local
        val currentSSID = getCurrentSSID() ?: return false
        return currentSSID == homeSSID
    }

    fun getCurrentSSID(): String? {
        // Try WifiManager first — most reliable across devices
        @Suppress("DEPRECATION")
        val wm = applicationContext.getSystemService(Context.WIFI_SERVICE) as android.net.wifi.WifiManager
        @Suppress("DEPRECATION")
        val ssid = wm.connectionInfo?.ssid?.removeSurrounding("\"")?.takeIf { it != "<unknown ssid>" && it.isNotBlank() }
        if (ssid != null) return ssid

        // Fallback: NetworkCapabilities (Android Q+)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            val cm = getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
            val network = cm.activeNetwork ?: return null
            val caps = cm.getNetworkCapabilities(network) ?: return null
            if (!caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI)) return null
            val wifiInfo = caps.transportInfo as? WifiInfo
            return wifiInfo?.ssid?.removeSurrounding("\"")?.takeIf { it != "<unknown ssid>" }
        }
        return null
    }

    private fun startNetworkMonitor() {
        val cm = getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        val request = NetworkRequest.Builder()
            .addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
            .addTransportType(NetworkCapabilities.TRANSPORT_CELLULAR)
            .build()
        val callback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) { applyServerForCurrentNetwork() }
            override fun onLost(network: Network) { applyServerForCurrentNetwork() }
            override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) {
                applyServerForCurrentNetwork()
            }
        }
        networkCallback = callback
        cm.registerNetworkCallback(request, callback)
    }

    fun updateServer(server: String) {
        getSharedPreferences("subclerk", Context.MODE_PRIVATE)
            .edit().putString("server", server).apply()
        applyServerForCurrentNetwork()
    }

    companion object {
        lateinit var instance: SubclerkApp
            private set
    }
}
