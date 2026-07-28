package io.github.sunrioa.rin.companion;

import com.google.gson.Gson;
import com.google.gson.GsonBuilder;
import com.google.gson.JsonElement;
import com.google.gson.ToNumberPolicy;
import com.google.gson.reflect.TypeToken;
import com.google.gson.stream.JsonReader;
import com.google.gson.stream.JsonToken;
import io.github.sunrioa.rin.JsonCodec;
import java.io.IOException;
import java.io.StringReader;
import java.lang.reflect.Type;
import java.util.HashSet;
import java.util.Map;
import java.util.Set;

final class GsonJsonCodec implements JsonCodec {
    private static final Type OBJECT_MAP = new TypeToken<Map<String, Object>>() { }.getType();
    private final Gson gson = new GsonBuilder().setObjectToNumberStrategy(ToNumberPolicy.LONG_OR_DOUBLE).create();
    @Override public String encode(Map<String, ?> value) { return gson.toJson(value); }
    @Override public Map<String, Object> decodeObject(String json) {
        rejectDuplicateKeys(json);
        JsonElement root = gson.fromJson(json, JsonElement.class);
        if (root == null || !root.isJsonObject()) throw new IllegalArgumentException("Rin envelope must be an object");
        return gson.fromJson(root, OBJECT_MAP);
    }

    private static void rejectDuplicateKeys(String json) {
        try (JsonReader reader = new JsonReader(new StringReader(json))) {
            scan(reader);
            if (reader.peek() != JsonToken.END_DOCUMENT) throw new IllegalArgumentException("Rin JSON has trailing content");
        } catch (IOException exception) {
            throw new IllegalArgumentException("Rin JSON is malformed", exception);
        }
    }

    private static void scan(JsonReader reader) throws IOException {
        switch (reader.peek()) {
            case BEGIN_OBJECT -> {
                reader.beginObject();
                Set<String> names = new HashSet<>();
                while (reader.hasNext()) {
                    if (!names.add(reader.nextName())) throw new IllegalArgumentException("Rin JSON has duplicate keys");
                    scan(reader);
                }
                reader.endObject();
            }
            case BEGIN_ARRAY -> {
                reader.beginArray();
                while (reader.hasNext()) scan(reader);
                reader.endArray();
            }
            case STRING -> reader.nextString();
            case NUMBER -> reader.nextString();
            case BOOLEAN -> reader.nextBoolean();
            case NULL -> reader.nextNull();
            default -> throw new IllegalArgumentException("Rin JSON is malformed");
        }
    }
}
