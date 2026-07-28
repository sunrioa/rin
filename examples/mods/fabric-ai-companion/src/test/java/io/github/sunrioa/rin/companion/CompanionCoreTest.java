package io.github.sunrioa.rin.companion;

import java.nio.file.Files;
import java.nio.file.Path;

public final class CompanionCoreTest {
    private CompanionCoreTest() {
    }

    public static void main(String[] args) throws Exception {
        require(CompanionChat.parse("@伙伴 你好").orElseThrow().equals("你好"),
                "Chinese companion chat was not parsed");
        require(CompanionChat.parse("普通聊天").isEmpty(),
                "ordinary chat was intercepted");
        require(CompanionChat.parse("@伙伴   ").isEmpty(),
                "empty companion chat was accepted");

        CompanionModelConfig defaults = CompanionModelConfig.defaults();
        require(defaults.baseUrl().toString().equals("https://api.deepseek.com/v1"),
                "unexpected DeepSeek default");
        require(defaults.model().equals("deepseek-chat"), "unexpected model default");
        requireThrows(() -> CompanionModelConfig.create("http://example.com/v1", "x"));
        requireThrows(() -> CompanionModelConfig.create("https://user@example.com/v1", "x"));
        requireThrows(() -> CompanionModelConfig.create("https://example.com/v1?q=x", "x"));
        requireThrows(() -> CompanionModelConfig.create("https://example.com/v1", "bad model"));

        Path configPath = Files.createTempDirectory("rin-companion-test")
                .resolve("rin-ai-companion.properties");
        CompanionConfigStore configStore = new CompanionConfigStore(configPath);
        configStore.save(CompanionModelConfig.create(
                "https://api.deepseek.com/v1", "deepseek-chat"));
        require(configStore.load().equals(CompanionModelConfig.defaults()),
                "model config did not round-trip");
    }

    private static void require(boolean condition, String message) {
        if (!condition) {
            throw new AssertionError(message);
        }
    }

    private static void requireThrows(ThrowingRunnable action) throws Exception {
        try {
            action.run();
        } catch (IllegalArgumentException expected) {
            return;
        }
        throw new AssertionError("expected IllegalArgumentException");
    }

    @FunctionalInterface
    private interface ThrowingRunnable {
        void run() throws Exception;
    }
}
