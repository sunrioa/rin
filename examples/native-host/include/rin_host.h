#ifndef RIN_HOST_H
#define RIN_HOST_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define RIN_HOST_MAX_CAPABILITIES 16u
#define RIN_HOST_MAX_OPERATIONS 32u
#define RIN_HOST_SHA256_LENGTH 64u

typedef enum {
    RIN_HOST_OK = 0,
    RIN_HOST_INVALID,
    RIN_HOST_CAPABILITY_MISSING,
    RIN_HOST_CAPABILITY_CHANGED,
    RIN_HOST_CAPABILITY_REVOKED,
    RIN_HOST_STALE_EPOCH,
    RIN_HOST_EXPIRED,
    RIN_HOST_ARGUMENTS_REJECTED,
    RIN_HOST_CAPACITY_EXCEEDED,
    RIN_HOST_DUPLICATE_OPERATION
} rin_host_result;

typedef enum {
    RIN_ACTION_QUEUED = 0,
    RIN_ACTION_RUNNING,
    RIN_ACTION_SUCCEEDED,
    RIN_ACTION_FAILED,
    RIN_ACTION_CANCELLED,
    RIN_ACTION_INTERRUPTED,
    RIN_ACTION_STALE,
    RIN_ACTION_OUTCOME_UNKNOWN
} rin_action_status;

typedef struct {
    const char *session_id;
    const char *world_id;
    uint64_t host;
    uint64_t world;
    uint64_t timeline;
} rin_epoch;

typedef int (*rin_validate_arguments)(
    const unsigned char *arguments,
    size_t arguments_size
);

typedef struct {
    const char *id;
    const char *version;
    const char *digest;
    uint64_t execution_budget;
    size_t max_input_bytes;
    int active;
    rin_validate_arguments validate_arguments;
} rin_capability;

typedef struct {
    rin_capability entries[RIN_HOST_MAX_CAPABILITIES];
    size_t count;
} rin_registry;

typedef struct {
    const char *offer_id;
    const char *decision_window_id;
    const char *actor_id;
    const char *capability_id;
    const char *capability_version;
    const char *descriptor_digest;
    const unsigned char *arguments;
    size_t arguments_size;
    rin_epoch expected_epoch;
    uint64_t observation_sequence;
    uint64_t deadline;
} rin_action_offer;

typedef struct {
    const char *operation_id;
    rin_action_offer offer;
    uint64_t deadline;
} rin_action_invocation;

typedef struct {
    const char *ids[RIN_HOST_MAX_OPERATIONS];
    size_t count;
} rin_operation_set;

void rin_registry_init(rin_registry *registry);
rin_host_result rin_registry_register(
    rin_registry *registry,
    const rin_capability *capability
);
rin_host_result rin_registry_revoke(
    rin_registry *registry,
    const char *id,
    const char *version
);
rin_host_result rin_bind_offer(
    const rin_registry *registry,
    const rin_action_offer *offer,
    const rin_epoch *current_epoch,
    uint64_t now,
    const char *operation_id,
    uint64_t invocation_deadline,
    rin_action_invocation *invocation
);
rin_host_result rin_authorize_invocation(
    const rin_registry *registry,
    const rin_action_invocation *invocation,
    const rin_epoch *current_epoch,
    uint64_t now
);
int rin_action_can_transition(rin_action_status from, rin_action_status to);
void rin_operation_set_init(rin_operation_set *operations);
rin_host_result rin_operation_begin(
    rin_operation_set *operations,
    const char *operation_id
);

#ifdef __cplusplus
}
#endif

#endif
