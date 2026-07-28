package io.github.sunrioa.rin.companion;

import java.io.BufferedWriter;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.AtomicMoveNotSupportedException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.nio.file.StandardOpenOption;
import java.util.HashSet;
import java.util.Set;

final class CompanionConfigStore {
    private static final long MAX_BYTES = 8 * 1024;
    private static final Set<String> KEYS = Set.of("base_url", "model");
    private final Path path;

    CompanionConfigStore(Path path) {
        this.path = path;
    }

    CompanionModelConfig load() {
        if (!Files.exists(path)) {
            return CompanionModelConfig.defaults();
        }
        try {
            if (Files.size(path) > MAX_BYTES) {
                throw invalid();
            }
            Set<String> seen = new HashSet<>();
            String baseUrl = null;
            String model = null;
            for (String line : Files.readAllLines(path, StandardCharsets.UTF_8)) {
                String trimmed = line.trim();
                if (trimmed.isEmpty() || trimmed.startsWith("#") || trimmed.startsWith("!")) {
                    continue;
                }
                int separator = trimmed.indexOf('=');
                if (separator <= 0) {
                    throw invalid();
                }
                String key = trimmed.substring(0, separator).trim();
                String value = trimmed.substring(separator + 1).trim();
                if (!KEYS.contains(key) || !seen.add(key)) {
                    throw invalid();
                }
                if (key.equals("base_url")) {
                    baseUrl = value;
                } else {
                    model = value;
                }
            }
            if (baseUrl == null || model == null) {
                throw invalid();
            }
            return CompanionModelConfig.create(baseUrl, model);
        } catch (IOException | RuntimeException exception) {
            if (exception instanceof IllegalArgumentException) {
                throw (IllegalArgumentException) exception;
            }
            throw new IllegalArgumentException("invalid companion model config", exception);
        }
    }

    void save(CompanionModelConfig config) {
        Path parent = path.toAbsolutePath().getParent();
        try {
            if (parent != null) {
                Files.createDirectories(parent);
            }
            Path temporary = Files.createTempFile(parent, path.getFileName().toString(), ".tmp");
            try {
                try (BufferedWriter writer = Files.newBufferedWriter(temporary, StandardCharsets.UTF_8,
                        StandardOpenOption.TRUNCATE_EXISTING)) {
                    writer.write("base_url=");
                    writer.write(config.baseUrl().toString());
                    writer.newLine();
                    writer.write("model=");
                    writer.write(config.model());
                    writer.newLine();
                }
                try {
                    Files.move(temporary, path, StandardCopyOption.ATOMIC_MOVE,
                            StandardCopyOption.REPLACE_EXISTING);
                } catch (AtomicMoveNotSupportedException exception) {
                    Files.move(temporary, path, StandardCopyOption.REPLACE_EXISTING);
                }
            } finally {
                Files.deleteIfExists(temporary);
            }
        } catch (IOException exception) {
            throw new IllegalStateException("could not save companion model config", exception);
        }
    }

    private static IllegalArgumentException invalid() {
        return new IllegalArgumentException("invalid companion model config");
    }
}
