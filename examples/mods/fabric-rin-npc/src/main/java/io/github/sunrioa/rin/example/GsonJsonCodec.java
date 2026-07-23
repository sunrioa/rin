package io.github.sunrioa.rin.example;

import com.google.gson.Gson;
import com.google.gson.JsonElement;
import com.google.gson.reflect.TypeToken;
import io.github.sunrioa.rin.JsonCodec;

import java.lang.reflect.Type;
import java.util.Map;

final class GsonJsonCodec implements JsonCodec {
    private static final Type OBJECT_MAP = new TypeToken<Map<String, Object>>() { }.getType();
    private final Gson gson;

    GsonJsonCodec(Gson gson) {
        this.gson = gson;
    }

    @Override
    public String encode(Map<String, ?> value) {
        return gson.toJson(value);
    }

    @Override
    public Map<String, Object> decodeObject(String json) {
        JsonElement root = gson.fromJson(json, JsonElement.class);
        if (root == null || !root.isJsonObject()) {
            throw new IllegalArgumentException("Rin envelope must be an object");
        }
        return gson.fromJson(root, OBJECT_MAP);
    }
}
