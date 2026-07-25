package io.github.sunrioa.rin;

public record HostCapabilities(
        int version,
        HostProfile profile,
        boolean stableIdentity,
        boolean durableBeforeNetwork,
        boolean durableOutbox,
        boolean idempotentApply,
        boolean atomicApplyAndOutbox) {

    public HostCapabilities {
        if (version != 1 || profile == null) {
            throw new RinConfigurationException(
                    "invalid_host_capabilities",
                    "Host capabilities have an unsupported version");
        }
        if (profile == HostProfile.IDEMPOTENT_ACTION &&
                !(stableIdentity && durableBeforeNetwork && durableOutbox && idempotentApply)) {
            throw new RinConfigurationException(
                    "invalid_host_capabilities",
                    "idempotent-action requires stable durable state, Outbox, and idempotent apply");
        }
        if (profile == HostProfile.TRANSACTIONAL_ACTION &&
                !(stableIdentity && durableBeforeNetwork && durableOutbox && atomicApplyAndOutbox)) {
            throw new RinConfigurationException(
                    "invalid_host_capabilities",
                    "transactional-action requires stable durable state, Outbox, and atomic settlement");
        }
    }

    public static HostCapabilities advisory() {
        return advisory(false);
    }

    public static HostCapabilities advisory(boolean stableIdentity) {
        return new HostCapabilities(
                1,
                HostProfile.ADVISORY,
                stableIdentity,
                false,
                false,
                false,
                false);
    }

    public static HostCapabilities idempotentAction() {
        return new HostCapabilities(
                1,
                HostProfile.IDEMPOTENT_ACTION,
                true,
                true,
                true,
                true,
                false);
    }

    public static HostCapabilities transactionalAction(boolean idempotentApply) {
        return new HostCapabilities(
                1,
                HostProfile.TRANSACTIONAL_ACTION,
                true,
                true,
                true,
                idempotentApply,
                true);
    }

    public void require(HostProfile requiredProfile) {
        if (requiredProfile == null) {
            throw new RinConfigurationException(
                    "invalid_host_profile",
                    "Required host profile is unknown");
        }
        if (profile.rank() < requiredProfile.rank()) {
            throw new RinConfigurationException(
                    "host_capability_insufficient",
                    "Action requires " + requiredProfile.label() +
                            ", but host provides " + profile.label());
        }
    }
}
