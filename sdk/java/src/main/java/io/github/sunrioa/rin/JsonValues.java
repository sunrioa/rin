package io.github.sunrioa.rin;

import java.lang.reflect.Array;
import java.math.BigDecimal;
import java.math.BigInteger;
import java.util.ArrayList;
import java.util.Collections;
import java.util.IdentityHashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

/** JSON-semantic comparisons for values crossing codecs or persistence layers. */
public final class JsonValues {
    private static final long MAX_SAFE_INTEGER = 9_007_199_254_740_991L;
    private static final int MAX_DEPTH = 64;

    private JsonValues() { }

    public static boolean equivalent(Object left, Object right) {
        if (left instanceof Number leftNumber && right instanceof Number rightNumber) {
            try {
                return new BigDecimal(leftNumber.toString())
                        .compareTo(new BigDecimal(rightNumber.toString())) == 0;
            } catch (NumberFormatException invalid) {
                return false;
            }
        }
        if (left instanceof Map<?, ?> leftMap && right instanceof Map<?, ?> rightMap) {
            if (!leftMap.keySet().equals(rightMap.keySet())) return false;
            for (Object key : leftMap.keySet()) {
                if (!equivalent(leftMap.get(key), rightMap.get(key))) return false;
            }
            return true;
        }
        if (left instanceof List<?> leftList && right instanceof List<?> rightList) {
            if (leftList.size() != rightList.size()) return false;
            for (int index = 0; index < leftList.size(); index++) {
                if (!equivalent(leftList.get(index), rightList.get(index))) return false;
            }
            return true;
        }
        return Objects.equals(left, right);
    }

    /** Rejects values that cannot cross Rin's strict JSON boundary safely. */
    public static void validateRequest(Object value) {
        validateRequest(value, 0, new IdentityHashMap<>());
    }

    /** Returns an immutable defensive copy of one JSON-compatible object. */
    @SuppressWarnings("unchecked")
    public static Map<String, Object> copyObject(Map<String, ?> value) {
        if (value == null) {
            throw new RinConfigurationException(
                    "invalid_json",
                    "Rin JSON object is required");
        }
        return (Map<String, Object>) copyValue(value);
    }

    private static void validateRequest(
            Object value,
            int depth,
            IdentityHashMap<Object, Boolean> active) {
        if (depth > MAX_DEPTH) {
            throw new RinProtocolException(
                    "invalid_request",
                    "Rin payload exceeds the JSON nesting limit");
        }
        if (value == null || value instanceof Boolean) return;
        if (value instanceof String text) {
            if (hasUnpairedSurrogate(text)) {
                throw new RinProtocolException(
                        "invalid_request",
                        "Rin payload contains invalid Unicode");
            }
            return;
        }
        if (value instanceof Number number) {
            validateNumber(number);
            return;
        }
        boolean container = value instanceof Map<?, ?> ||
                value instanceof List<?> || value.getClass().isArray();
        if (!container) {
            throw new RinProtocolException(
                    "invalid_request",
                    "Rin payload contains a non-JSON value");
        }
        if (active.put(value, Boolean.TRUE) != null) {
            throw new RinProtocolException(
                    "invalid_request",
                    "Rin payload contains a JSON cycle");
        }
        try {
            if (value instanceof Map<?, ?> map) {
                for (Map.Entry<?, ?> entry : map.entrySet()) {
                    if (!(entry.getKey() instanceof String key)) {
                        throw new RinProtocolException(
                                "invalid_request",
                                "Rin payload contains a non-string JSON object key");
                    }
                    if (hasUnpairedSurrogate(key)) {
                        throw new RinProtocolException(
                                "invalid_request",
                                "Rin payload contains invalid Unicode");
                    }
                    validateRequest(entry.getValue(), depth + 1, active);
                }
            } else if (value instanceof List<?> list) {
                for (Object child : list) validateRequest(child, depth + 1, active);
            } else {
                int length = Array.getLength(value);
                for (int index = 0; index < length; index++) {
                    validateRequest(Array.get(value, index), depth + 1, active);
                }
            }
        } finally {
            active.remove(value);
        }
    }

    private static void validateNumber(Number value) {
        if (value instanceof Byte || value instanceof Short ||
                value instanceof Integer || value instanceof Long) {
            long number = value.longValue();
            if (number < -MAX_SAFE_INTEGER || number > MAX_SAFE_INTEGER) {
                throw unsafeInteger();
            }
            return;
        }
        if (value instanceof BigInteger integer) {
            BigInteger maximum = BigInteger.valueOf(MAX_SAFE_INTEGER);
            if (integer.compareTo(maximum.negate()) < 0 || integer.compareTo(maximum) > 0) {
                throw unsafeInteger();
            }
            return;
        }
        BigDecimal decimal;
        if (value instanceof BigDecimal exact) {
            decimal = exact;
        } else if (value instanceof Double number) {
            if (!Double.isFinite(number)) throw nonFiniteNumber();
            decimal = BigDecimal.valueOf(number);
        } else if (value instanceof Float number) {
            if (!Float.isFinite(number)) throw nonFiniteNumber();
            decimal = new BigDecimal(Float.toString(number));
        } else {
            try {
                decimal = new BigDecimal(value.toString());
            } catch (NumberFormatException exception) {
                throw new RinProtocolException(
                        "invalid_request",
                        "Rin payload contains a non-finite JSON number",
                        exception);
            }
        }
        if (decimal.stripTrailingZeros().scale() <= 0) {
            BigDecimal maximum = BigDecimal.valueOf(MAX_SAFE_INTEGER);
            if (decimal.compareTo(maximum.negate()) < 0 || decimal.compareTo(maximum) > 0) {
                throw unsafeInteger();
            }
        }
    }

    private static RinProtocolException unsafeInteger() {
        return new RinProtocolException(
                "invalid_request",
                "Rin payload contains an unsafe JSON integer");
    }

    private static RinProtocolException nonFiniteNumber() {
        return new RinProtocolException(
                "invalid_request",
                "Rin payload contains a non-finite JSON number");
    }

    private static Object copyValue(Object value) {
        if (value == null || value instanceof String ||
                value instanceof Number || value instanceof Boolean) {
            return value;
        }
        if (value instanceof Map<?, ?> source) {
            Map<String, Object> result = new LinkedHashMap<>();
            for (Map.Entry<?, ?> entry : source.entrySet()) {
                if (!(entry.getKey() instanceof String key)) {
                    throw new RinConfigurationException(
                            "invalid_json",
                            "Rin JSON object keys must be strings");
                }
                result.put(key, copyValue(entry.getValue()));
            }
            return Collections.unmodifiableMap(result);
        }
        if (value instanceof List<?> source) {
            List<Object> result = new ArrayList<>(source.size());
            for (Object child : source) result.add(copyValue(child));
            return Collections.unmodifiableList(result);
        }
        if (value.getClass().isArray()) {
            int length = Array.getLength(value);
            List<Object> result = new ArrayList<>(length);
            for (int index = 0; index < length; index++) {
                result.add(copyValue(Array.get(value, index)));
            }
            return Collections.unmodifiableList(result);
        }
        throw new RinConfigurationException(
                "invalid_json",
                "Rin JSON value contains a non-JSON type");
    }

    private static boolean hasUnpairedSurrogate(String value) {
        for (int index = 0; index < value.length(); index++) {
            char current = value.charAt(index);
            if (Character.isHighSurrogate(current)) {
                if (index + 1 >= value.length() ||
                        !Character.isLowSurrogate(value.charAt(index + 1))) {
                    return true;
                }
                index++;
            } else if (Character.isLowSurrogate(current)) {
                return true;
            }
        }
        return false;
    }
}
