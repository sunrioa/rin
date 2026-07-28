package io.github.sunrioa.rin.companion;

import java.util.Set;

final class CompanionActions {
    private static final Set<String> ALLOWED = Set.of(
            "dialogue.reply", "movement.follow_owner", "movement.stop", "activity.wait", "safety.refuse");

    private CompanionActions() {
    }

    static String requireAllowed(String capability) {
        if (!ALLOWED.contains(capability)) {
            throw new IllegalArgumentException("unknown companion action");
        }
        return capability;
    }
}
