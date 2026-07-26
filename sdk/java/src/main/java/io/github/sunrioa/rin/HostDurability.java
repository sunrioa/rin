package io.github.sunrioa.rin;

public record HostDurability(
        int version,
        HostDurabilityProfile profile,
        boolean stableIdentity,
        boolean durableBeforeNetwork,
        boolean durableOutbox,
        boolean idempotentApply,
        boolean atomicApplyAndOutbox) {

    public HostDurability {
        if (version != 1 || profile == null) {
            throw new RinConfigurationException(
                    "invalid_host_durability",
                    "Host durability has an unsupported version");
        }
        if (profile == HostDurabilityProfile.IDEMPOTENT_ACTION &&
                !(stableIdentity && durableBeforeNetwork && durableOutbox && idempotentApply)) {
            throw new RinConfigurationException(
                    "invalid_host_durability",
                    "idempotent-action requires stable durable state, Outbox, and idempotent apply");
        }
        if (profile == HostDurabilityProfile.TRANSACTIONAL_ACTION &&
                !(stableIdentity && durableBeforeNetwork && durableOutbox && atomicApplyAndOutbox)) {
            throw new RinConfigurationException(
                    "invalid_host_durability",
                    "transactional-action requires stable durable state, Outbox, and atomic settlement");
        }
    }

    public static HostDurability advisory() {
        return advisory(false);
    }

    public static HostDurability advisory(boolean stableIdentity) {
        return new HostDurability(
                1,
                HostDurabilityProfile.ADVISORY,
                stableIdentity,
                false,
                false,
                false,
                false);
    }

    public static HostDurability idempotentAction() {
        return new HostDurability(
                1,
                HostDurabilityProfile.IDEMPOTENT_ACTION,
                true,
                true,
                true,
                true,
                false);
    }

    public static HostDurability transactionalAction(boolean idempotentApply) {
        return new HostDurability(
                1,
                HostDurabilityProfile.TRANSACTIONAL_ACTION,
                true,
                true,
                true,
                idempotentApply,
                true);
    }

    public void require(HostDurabilityProfile requiredDurability) {
        if (requiredDurability == null) {
            throw new RinConfigurationException(
                    "invalid_host_durability_profile",
                    "Required host durability profile is unknown");
        }
        if (profile.rank() < requiredDurability.rank()) {
            throw new RinConfigurationException(
                    "host_durability_insufficient",
                    "Action requires " + requiredDurability.label() +
                            ", but host provides " + profile.label());
        }
    }
}
