package io.github.sunrioa.rin;

import java.util.Map;

/** JSON boundary that can decode either an object or an array. */
public interface JsonValueCodec {
    String encodeObject(Map<String, ?> value) throws Exception;

    Object decodeValue(String json) throws Exception;
}
