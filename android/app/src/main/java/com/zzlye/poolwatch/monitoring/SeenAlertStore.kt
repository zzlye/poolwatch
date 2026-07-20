package com.zzlye.poolwatch.monitoring

import android.content.Context
import org.json.JSONArray

class SeenAlertStore(context: Context) {
    private val preferences = context.applicationContext.getSharedPreferences(
        PREFERENCES_NAME,
        Context.MODE_PRIVATE,
    )
    private val lock = Any()

    fun contains(id: String): Boolean = synchronized(lock) {
        readIds().contains(id)
    }

    fun mark(id: String) {
        markAll(listOf(id))
    }

    fun markAll(ids: Collection<String>) = synchronized(lock) {
        val merged = LinkedHashSet(readIds())
        ids.asSequence().map(String::trim).filter(String::isNotEmpty).forEach {
            merged.remove(it)
            merged.add(it)
        }
        while (merged.size > MAX_IDS) {
            merged.remove(merged.first())
        }
        val encoded = JSONArray()
        merged.forEach(encoded::put)
        preferences.edit().putString(KEY_IDS, encoded.toString()).apply()
    }

    fun clear() = synchronized(lock) {
        preferences.edit().remove(KEY_IDS).apply()
    }

    private fun readIds(): List<String> {
        val encoded = preferences.getString(KEY_IDS, "[]") ?: "[]"
        return runCatching {
            val array = JSONArray(encoded)
            buildList {
                for (index in 0 until array.length()) {
                    array.optString(index).takeIf(String::isNotBlank)?.let(::add)
                }
            }
        }.getOrDefault(emptyList())
    }

    companion object {
        private const val PREFERENCES_NAME = "poolwatch_seen_alerts"
        private const val KEY_IDS = "ids"
        private const val MAX_IDS = 1_000
    }
}
