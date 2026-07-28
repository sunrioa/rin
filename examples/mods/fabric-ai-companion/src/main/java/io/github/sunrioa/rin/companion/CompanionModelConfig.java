package io.github.sunrioa.rin.companion;

import java.net.URI;
import java.util.Objects;
import java.util.Set;
import java.util.regex.Pattern;

record CompanionModelConfig(URI baseUrl, String model) {
    private static final Pattern MODEL = Pattern.compile("[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}");
    private static final Set<String> LOOPBACK_HOSTS = Set.of("localhost", "127.0.0.1", "::1", "[::1]");

    CompanionModelConfig {
        Objects.requireNonNull(baseUrl, "baseUrl");
        Objects.requireNonNull(model, "model");
    }

    static CompanionModelConfig defaults() {
        return create("https://api.deepseek.com/v1", "deepseek-chat");
    }

    static CompanionModelConfig create(String baseUrl, String model) {
        final URI uri;
        try {
            uri = URI.create(baseUrl);
        } catch (IllegalArgumentException exception) {
            throw new IllegalArgumentException("invalid companion model config", exception);
        }
        String host = uri.getHost();
        boolean loopback = host != null && LOOPBACK_HOSTS.contains(host);
        boolean schemeAllowed = "https".equalsIgnoreCase(uri.getScheme()) ||
                ("http".equalsIgnoreCase(uri.getScheme()) && loopback);
        if (!schemeAllowed || host == null || host.isBlank() || uri.getUserInfo() != null ||
                uri.getQuery() != null || uri.getFragment() != null || !MODEL.matcher(model).matches()) {
            throw new IllegalArgumentException("invalid companion model config");
        }
        return new CompanionModelConfig(uri, model);
    }
}
