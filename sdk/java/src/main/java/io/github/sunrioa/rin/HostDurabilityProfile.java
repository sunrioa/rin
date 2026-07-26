package io.github.sunrioa.rin;

public enum HostDurabilityProfile {
    ADVISORY("advisory", 0),
    IDEMPOTENT_ACTION("idempotent-action", 1),
    TRANSACTIONAL_ACTION("transactional-action", 2);

    private final String label;
    private final int rank;

    HostDurabilityProfile(String label, int rank) {
        this.label = label;
        this.rank = rank;
    }

    public String label() {
        return label;
    }

    int rank() {
        return rank;
    }
}
