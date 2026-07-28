package io.github.sunrioa.rin.companion;

import java.util.Optional;

final class CompanionChat {
    private static final String PREFIX = "@伙伴";
    private static final int MAX_CODE_POINTS = 2_000;

    private CompanionChat() {
    }

    static Optional<String> parse(String message) {
        if (message == null || !message.startsWith(PREFIX)) {
            return Optional.empty();
        }
        String text = message.substring(PREFIX.length()).trim();
        if (text.isEmpty() || text.codePointCount(0, text.length()) > MAX_CODE_POINTS ||
                text.codePoints().anyMatch(Character::isISOControl)) {
            return Optional.empty();
        }
        return Optional.of(text);
    }
}
