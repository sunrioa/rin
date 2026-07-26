#include "rin_host.h"

#include <stdio.h>
#include <string.h>

#define CHECK(value) do { \
    if (!(value)) { \
        fprintf(stderr, "check failed at line %d: %s\n", __LINE__, #value); \
        return 1; \
    } \
} while (0)

static int dialogue_arguments(
    const unsigned char *arguments,
    size_t arguments_size
) {
    static const char expected[] = "{\"line_id\":\"greeting\"}";
    return arguments != NULL &&
           arguments_size == sizeof(expected) - 1u &&
           memcmp(arguments, expected, arguments_size) == 0;
}

int main(void) {
    static const char digest[] =
        "0123456789abcdef0123456789abcdef"
        "0123456789abcdef0123456789abcdef";
    static const unsigned char arguments[] =
        "{\"line_id\":\"greeting\"}";
    rin_registry registry;
    rin_operation_set operations;
    rin_epoch epoch = {"session.test", "world.test", 1u, 1u, 1u};
    rin_epoch next_epoch = {"session.test", "world.test", 1u, 2u, 1u};
    rin_capability capability = {
        "dialogue.say",
        "1.0.0",
        digest,
        5u,
        128u,
        1,
        dialogue_arguments
    };
    rin_action_offer offer = {
        "offer.test",
        "window.test",
        "actor.test",
        "dialogue.say",
        "1.0.0",
        digest,
        arguments,
        sizeof(arguments) - 1u,
        {"session.test", "world.test", 1u, 1u, 1u},
        1u,
        20u
    };
    rin_action_invocation invocation;
    int effect_count = 0;

    rin_registry_init(&registry);
    rin_operation_set_init(&operations);
    CHECK(rin_registry_register(&registry, &capability) == RIN_HOST_OK);
    CHECK(rin_bind_offer(
        &registry,
        &offer,
        &epoch,
        10u,
        "operation.test",
        15u,
        &invocation
    ) == RIN_HOST_OK);
    CHECK(rin_authorize_invocation(
        &registry,
        &invocation,
        &epoch,
        11u
    ) == RIN_HOST_OK);
    CHECK(rin_operation_begin(
        &operations,
        invocation.operation_id
    ) == RIN_HOST_OK);
    effect_count += 1;
    CHECK(rin_operation_begin(
        &operations,
        invocation.operation_id
    ) == RIN_HOST_DUPLICATE_OPERATION);
    CHECK(effect_count == 1);
    puts("idempotent_operation=pass");

    CHECK(rin_authorize_invocation(
        &registry,
        &invocation,
        &next_epoch,
        11u
    ) == RIN_HOST_STALE_EPOCH);
    puts("stale_epoch_rejection=pass");

    CHECK(rin_registry_revoke(
        &registry,
        "dialogue.say",
        "1.0.0"
    ) == RIN_HOST_OK);
    CHECK(rin_authorize_invocation(
        &registry,
        &invocation,
        &epoch,
        11u
    ) == RIN_HOST_CAPABILITY_REVOKED);
    puts("revoked_capability_rejection=pass");

    CHECK(rin_action_can_transition(
        RIN_ACTION_QUEUED,
        RIN_ACTION_RUNNING
    ));
    CHECK(rin_action_can_transition(
        RIN_ACTION_RUNNING,
        RIN_ACTION_SUCCEEDED
    ));
    CHECK(!rin_action_can_transition(
        RIN_ACTION_SUCCEEDED,
        RIN_ACTION_RUNNING
    ));
    puts("action_run_monotonicity=pass");
    return 0;
}
