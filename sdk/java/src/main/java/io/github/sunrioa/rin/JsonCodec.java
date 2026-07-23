package io.github.sunrioa.rin;

import java.util.Map;

/** JSON boundary supplied by the host engine (for example Gson or Jackson). */
public interface JsonCodec {
    String encode(Map<String, ?> value) throws Exception;

    Map<String, Object> decodeObject(String json) throws Exception;
}
