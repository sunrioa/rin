package io.github.sunrioa.rin.companion;

import io.github.sunrioa.rin.RinClient;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.CompletableFuture;

final class CompanionDialogue {
    private CompanionDialogue() {
    }

    static String parse(String json) {
        Map<String, Object> object = new GsonJsonCodec().decodeObject(json);
        if (!object.keySet().equals(Set.of("line"))) throw new IllegalArgumentException("invalid dialogue shape");
        Object value = object.get("line");
        if (!(value instanceof String line) || line.isBlank() || line.codePointCount(0, line.length()) > 300 ||
                line.codePoints().anyMatch(Character::isISOControl)) {
            throw new IllegalArgumentException("invalid dialogue line");
        }
        return line;
    }

    static String fallback(String actionId) {
        return switch (actionId) {
            case "movement.follow_owner" -> "好，我跟着你。";
            case "movement.stop" -> "好，我停在这里。";
            case "activity.wait" -> "好，我们先观察一下。";
            case "safety.refuse" -> "这个请求不安全，我不能这样做。";
            default -> "好的，我听到了。";
        };
    }

    static CompletableFuture<String> generate(RinClient client, String requestId, String ownerMessage,
                                               String actionId, String mode, String worldSummary) {
        String semantic = ownerMessage + "\n" + actionId + "\n" + mode + "\n" + worldSummary;
        Map<String, Object> request = Map.of(
                "protocol_version", RinClient.PROTOCOL_VERSION,
                "request_id", requestId,
                "kind", "free-response",
                "context_hash", sha256(semantic),
                "messages", List.of(
                        Map.of("role", "system", "content", "只输出 JSON 对象，且只能包含一个字符串字段 line；使用简洁中文。"),
                        Map.of("role", "user", "content", semantic)),
                "temperature", 0.6,
                "max_tokens", 192,
                "response_format", "json_object");
        return client.submitGenerationJob(request)
                .thenCompose(job -> {
                    Object id = job.get("job_id");
                    if (!(id instanceof String jobId)) {
                        return CompletableFuture.failedFuture(new IllegalArgumentException("generation job id missing"));
                    }
                    return client.waitForGeneration(jobId);
                })
                .thenApply(job -> {
                    Object rawResult = job.get("result");
                    if (!(rawResult instanceof Map<?, ?> result) || !(result.get("content") instanceof String content)) {
                        throw new IllegalArgumentException("generation result missing content");
                    }
                    return parse(content);
                })
                .exceptionally(ignored -> fallback(actionId));
    }

    private static String sha256(String text) {
        try {
            byte[] digest = MessageDigest.getInstance("SHA-256").digest(text.getBytes(StandardCharsets.UTF_8));
            return java.util.HexFormat.of().formatHex(digest);
        } catch (NoSuchAlgorithmException impossible) {
            throw new IllegalStateException("SHA-256 is unavailable", impossible);
        }
    }
}
