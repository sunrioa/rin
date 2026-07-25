package io.github.sunrioa.rin;

import java.lang.reflect.Array;
import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

public record PendingTurn(
        int version,
        String operationId,
        Map<String, Object> request,
        String jobId) {

    public PendingTurn {
        if (version != 1 ||
                !RinClient.isProtocolIdentifier(operationId) ||
                request == null ||
                !RinClient.isProtocolIdentifier(request.get("request_id")) ||
                !RinClient.isProtocolIdentifier(request.get("session_id")) ||
                (jobId != null && !jobId.isEmpty() && !RinClient.isProtocolIdentifier(jobId))) {
            throw new RinConfigurationException(
                    "invalid_pending_turn",
                    "Pending Turn is missing or malformed");
        }
        RinClient.validateRequestJson(request);
        request = copyObject(request);
        jobId = jobId == null ? "" : jobId;
    }

    public static PendingTurn create(String operationId, Map<String, ?> request) {
        return new PendingTurn(1, operationId, copyObject(request), "");
    }

    public PendingTurn withJobId(String value) {
        return new PendingTurn(version, operationId, request, value);
    }

    @SuppressWarnings("unchecked")
    static Map<String, Object> copyObject(Map<String, ?> value) {
        if (value == null) {
            throw new RinConfigurationException(
                    "invalid_workflow",
                    "Workflow value must be a JSON object");
        }
        return (Map<String, Object>) copyValue(value);
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
                            "invalid_workflow",
                            "Workflow object keys must be strings");
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
                "invalid_workflow",
                "Workflow value contains a non-JSON type");
    }
}
