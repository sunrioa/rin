#include "rin_host.h"

#include <string.h>

static int rin_text_present(const char *value) {
    return value != NULL && value[0] != '\0';
}

static int rin_epoch_valid(const rin_epoch *epoch) {
    return epoch != NULL &&
           rin_text_present(epoch->session_id) &&
           rin_text_present(epoch->world_id) &&
           epoch->host > 0u &&
           epoch->world > 0u &&
           epoch->timeline > 0u;
}

static int rin_epoch_equal(const rin_epoch *left, const rin_epoch *right) {
    return rin_epoch_valid(left) &&
           rin_epoch_valid(right) &&
           strcmp(left->session_id, right->session_id) == 0 &&
           strcmp(left->world_id, right->world_id) == 0 &&
           left->host == right->host &&
           left->world == right->world &&
           left->timeline == right->timeline;
}

static int rin_digest_valid(const char *digest) {
    size_t index;
    if (digest == NULL || strlen(digest) != RIN_HOST_SHA256_LENGTH) {
        return 0;
    }
    for (index = 0u; index < RIN_HOST_SHA256_LENGTH; ++index) {
        const char value = digest[index];
        if (!((value >= '0' && value <= '9') ||
              (value >= 'a' && value <= 'f'))) {
            return 0;
        }
    }
    return 1;
}

static const rin_capability *rin_registry_find(
    const rin_registry *registry,
    const char *id,
    const char *version
) {
    size_t index;
    if (registry == NULL ||
        registry->count > RIN_HOST_MAX_CAPABILITIES ||
        !rin_text_present(id) ||
        !rin_text_present(version)) {
        return NULL;
    }
    for (index = 0u; index < registry->count; ++index) {
        const rin_capability *entry = &registry->entries[index];
        if (strcmp(entry->id, id) == 0 &&
            strcmp(entry->version, version) == 0) {
            return entry;
        }
    }
    return NULL;
}

static rin_host_result rin_validate_offer(
    const rin_registry *registry,
    const rin_action_offer *offer,
    const rin_epoch *current_epoch,
    uint64_t now
) {
    const rin_capability *capability;
    if (offer == NULL ||
        !rin_text_present(offer->offer_id) ||
        !rin_text_present(offer->decision_window_id) ||
        !rin_text_present(offer->actor_id) ||
        !rin_text_present(offer->capability_id) ||
        !rin_text_present(offer->capability_version) ||
        !rin_digest_valid(offer->descriptor_digest) ||
        (offer->arguments_size > 0u && offer->arguments == NULL) ||
        offer->observation_sequence == 0u) {
        return RIN_HOST_INVALID;
    }
    if (offer->deadline <= now) {
        return RIN_HOST_EXPIRED;
    }
    if (!rin_epoch_equal(&offer->expected_epoch, current_epoch)) {
        return RIN_HOST_STALE_EPOCH;
    }
    capability = rin_registry_find(
        registry,
        offer->capability_id,
        offer->capability_version
    );
    if (capability == NULL) {
        return RIN_HOST_CAPABILITY_MISSING;
    }
    if (!capability->active) {
        return RIN_HOST_CAPABILITY_REVOKED;
    }
    if (strcmp(capability->digest, offer->descriptor_digest) != 0) {
        return RIN_HOST_CAPABILITY_CHANGED;
    }
    if (offer->arguments_size > capability->max_input_bytes) {
        return RIN_HOST_ARGUMENTS_REJECTED;
    }
    if (capability->validate_arguments != NULL &&
        !capability->validate_arguments(
            offer->arguments,
            offer->arguments_size
        )) {
        return RIN_HOST_ARGUMENTS_REJECTED;
    }
    return RIN_HOST_OK;
}

void rin_registry_init(rin_registry *registry) {
    if (registry != NULL) {
        memset(registry, 0, sizeof(*registry));
    }
}

rin_host_result rin_registry_register(
    rin_registry *registry,
    const rin_capability *capability
) {
    const rin_capability *existing;
    if (registry == NULL ||
        registry->count > RIN_HOST_MAX_CAPABILITIES ||
        capability == NULL ||
        !rin_text_present(capability->id) ||
        !rin_text_present(capability->version) ||
        !rin_digest_valid(capability->digest) ||
        capability->execution_budget == 0u ||
        capability->max_input_bytes == 0u ||
        capability->active != 1) {
        return RIN_HOST_INVALID;
    }
    existing = rin_registry_find(
        registry,
        capability->id,
        capability->version
    );
    if (existing != NULL) {
        return strcmp(existing->digest, capability->digest) == 0
            ? RIN_HOST_OK
            : RIN_HOST_CAPABILITY_CHANGED;
    }
    if (registry->count == RIN_HOST_MAX_CAPABILITIES) {
        return RIN_HOST_CAPACITY_EXCEEDED;
    }
    registry->entries[registry->count++] = *capability;
    return RIN_HOST_OK;
}

rin_host_result rin_registry_revoke(
    rin_registry *registry,
    const char *id,
    const char *version
) {
    size_t index;
    if (registry == NULL ||
        registry->count > RIN_HOST_MAX_CAPABILITIES ||
        !rin_text_present(id) ||
        !rin_text_present(version)) {
        return RIN_HOST_INVALID;
    }
    for (index = 0u; index < registry->count; ++index) {
        rin_capability *capability = &registry->entries[index];
        if (strcmp(capability->id, id) == 0 &&
            strcmp(capability->version, version) == 0) {
            capability->active = 0;
            return RIN_HOST_OK;
        }
    }
    return RIN_HOST_CAPABILITY_MISSING;
}

rin_host_result rin_bind_offer(
    const rin_registry *registry,
    const rin_action_offer *offer,
    const rin_epoch *current_epoch,
    uint64_t now,
    const char *operation_id,
    uint64_t invocation_deadline,
    rin_action_invocation *invocation
) {
    const rin_capability *capability;
    rin_host_result result = rin_validate_offer(
        registry,
        offer,
        current_epoch,
        now
    );
    if (result != RIN_HOST_OK) {
        return result;
    }
    capability = rin_registry_find(
        registry,
        offer->capability_id,
        offer->capability_version
    );
    if (!rin_text_present(operation_id) ||
        invocation == NULL ||
        invocation_deadline <= now ||
        invocation_deadline > offer->deadline ||
        invocation_deadline - now > capability->execution_budget) {
        return RIN_HOST_INVALID;
    }
    invocation->operation_id = operation_id;
    invocation->offer = *offer;
    invocation->deadline = invocation_deadline;
    return RIN_HOST_OK;
}

rin_host_result rin_authorize_invocation(
    const rin_registry *registry,
    const rin_action_invocation *invocation,
    const rin_epoch *current_epoch,
    uint64_t now
) {
    rin_host_result result;
    if (invocation == NULL || now >= invocation->deadline) {
        return RIN_HOST_EXPIRED;
    }
    result = rin_validate_offer(
        registry,
        &invocation->offer,
        current_epoch,
        now
    );
    return result;
}

int rin_action_can_transition(rin_action_status from, rin_action_status to) {
    if (from == RIN_ACTION_QUEUED) {
        return to == RIN_ACTION_RUNNING ||
               to == RIN_ACTION_FAILED ||
               to == RIN_ACTION_CANCELLED ||
               to == RIN_ACTION_STALE ||
               to == RIN_ACTION_OUTCOME_UNKNOWN;
    }
    if (from == RIN_ACTION_RUNNING) {
        return to == RIN_ACTION_SUCCEEDED ||
               to == RIN_ACTION_FAILED ||
               to == RIN_ACTION_CANCELLED ||
               to == RIN_ACTION_INTERRUPTED ||
               to == RIN_ACTION_STALE ||
               to == RIN_ACTION_OUTCOME_UNKNOWN;
    }
    if (from == RIN_ACTION_OUTCOME_UNKNOWN) {
        return to == RIN_ACTION_SUCCEEDED ||
               to == RIN_ACTION_FAILED ||
               to == RIN_ACTION_CANCELLED ||
               to == RIN_ACTION_INTERRUPTED ||
               to == RIN_ACTION_STALE;
    }
    return 0;
}

void rin_operation_set_init(rin_operation_set *operations) {
    if (operations != NULL) {
        memset(operations, 0, sizeof(*operations));
    }
}

rin_host_result rin_operation_begin(
    rin_operation_set *operations,
    const char *operation_id
) {
    size_t index;
    if (operations == NULL ||
        operations->count > RIN_HOST_MAX_OPERATIONS ||
        !rin_text_present(operation_id)) {
        return RIN_HOST_INVALID;
    }
    for (index = 0u; index < operations->count; ++index) {
        if (strcmp(operations->ids[index], operation_id) == 0) {
            return RIN_HOST_DUPLICATE_OPERATION;
        }
    }
    if (operations->count == RIN_HOST_MAX_OPERATIONS) {
        return RIN_HOST_CAPACITY_EXCEEDED;
    }
    operations->ids[operations->count++] = operation_id;
    return RIN_HOST_OK;
}
