#!/usr/bin/env python3
"""Verify Rin's decision model against real OpenSpiel game semantics."""

from __future__ import annotations

import hashlib
import importlib.metadata
import json
from dataclasses import dataclass
from typing import Any

import pyspiel


OPEN_SPIEL_VERSION = "2.0.1"
MAX_SAFE_INTEGER = 9_007_199_254_740_991


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def identifier(value: str) -> str:
    normalized = "".join(
        character if character.isalnum() or character in "._-" else "."
        for character in value.lower()
    )
    return normalized.strip(".")


@dataclass(frozen=True)
class Authority:
    session_id: str
    world_id: str
    host: int = 1
    world: int = 1
    timeline: int = 1

    def epoch(self) -> dict[str, Any]:
        return {
            "session_id": self.session_id,
            "world_id": self.world_id,
            "host": self.host,
            "world": self.world,
            "timeline": self.timeline,
        }


class HostProjection:
    """Small game-owned projection; it never executes model-authored code."""

    def __init__(self, game: pyspiel.Game, authority: Authority) -> None:
        self.game = game
        self.authority = authority
        self.game_id = identifier(game.get_type().short_name)
        descriptor = json.dumps(
            {
                "id": "openspiel.legal-action",
                "version": "1",
                "arguments": {
                    "type": "object",
                    "required": ["action"],
                    "properties": {"action": {"type": "integer"}},
                    "additionalProperties": False,
                },
            },
            sort_keys=True,
            separators=(",", ":"),
        ).encode()
        self.descriptor_digest = hashlib.sha256(descriptor).hexdigest()

    @staticmethod
    def tick(state: pyspiel.State) -> int:
        tick = state.move_number()
        require(0 <= tick <= MAX_SAFE_INTEGER, "OpenSpiel move number is not JSON-safe")
        return tick

    def decision_window(self, state: pyspiel.State) -> dict[str, Any] | None:
        if state.is_terminal() or state.is_chance_node():
            return None
        tick = self.tick(state)
        if state.is_simultaneous_node():
            mode = "simultaneous"
            actors = [
                f"player.{player}"
                for player in range(self.game.num_players())
                if state.legal_actions(player)
            ]
            require(bool(actors), "simultaneous node has no acting player")
        else:
            mode = "sequential"
            actors = [f"player.{state.current_player()}"]
        return {
            "id": f"window.{self.game_id}.{tick}",
            "mode": mode,
            "epoch": self.authority.epoch(),
            "observation_seq": tick + 1,
            "opened_at": {"clock": "step", "value": tick},
            "deadline": {"clock": "step", "value": tick + 1},
            "actor_ids": actors,
        }

    def offers(
        self, state: pyspiel.State, window: dict[str, Any]
    ) -> dict[str, list[dict[str, Any]]]:
        require(self.window_is_current(state, window), "cannot offer from a stale window")
        result: dict[str, list[dict[str, Any]]] = {}
        for actor in window["actor_ids"]:
            player = int(actor.removeprefix("player."))
            result[actor] = [
                {
                    "offer_id": f"{window['id']}.{actor}.action.{action}",
                    "decision_window_id": window["id"],
                    "actor_id": actor,
                    "capability": {"id": "openspiel.legal-action", "version": "1"},
                    "descriptor_digest": self.descriptor_digest,
                    "description": state.action_to_string(player, action),
                    "arguments": {"action": action},
                    "expected_epoch": self.authority.epoch(),
                    "observation_seq": window["observation_seq"],
                    "deadline": window["deadline"],
                }
                for action in state.legal_actions(player)
            ]
        return result

    def observation(self, state: pyspiel.State, player: int) -> dict[str, Any]:
        return {
            "actor_id": f"player.{player}",
            "tick": self.tick(state),
            "payload": {
                "information_state": state.information_state_string(player),
            },
        }

    def window_is_current(
        self, state: pyspiel.State, window: dict[str, Any]
    ) -> bool:
        current = self.decision_window(state)
        return current is not None and current == window

    def apply_sequential(
        self,
        state: pyspiel.State,
        window: dict[str, Any],
        actor: str,
        action: int,
    ) -> None:
        require(window["mode"] == "sequential", "joint window used as sequential")
        require(self.window_is_current(state, window), "stale sequential window")
        require(window["actor_ids"] == [actor], "actor does not own this turn")
        player = int(actor.removeprefix("player."))
        require(action in state.legal_actions(player), "illegal sequential action")
        state.apply_action(action)

    def apply_simultaneous(
        self,
        state: pyspiel.State,
        window: dict[str, Any],
        actions: dict[str, int],
    ) -> None:
        require(window["mode"] == "simultaneous", "sequential window used as joint")
        require(self.window_is_current(state, window), "stale simultaneous window")
        require(set(actions) == set(window["actor_ids"]), "joint action is incomplete")
        ordered: list[int] = []
        for player in range(self.game.num_players()):
            actor = f"player.{player}"
            legal = state.legal_actions(player)
            action = actions[actor] if legal else 0
            require(not legal or action in legal, "illegal joint action")
            ordered.append(action)
        state.apply_actions(ordered)

    @staticmethod
    def apply_chance(state: pyspiel.State, outcome: int) -> None:
        require(state.is_chance_node(), "game action was misclassified as chance")
        probabilities = dict(state.chance_outcomes())
        require(outcome in probabilities, "illegal chance outcome")
        state.apply_action(outcome)


def verify_sequential() -> None:
    game = pyspiel.load_game("tic_tac_toe")
    state = game.new_initial_state()
    host = HostProjection(game, Authority("session.sequential", "world.sequential"))
    window = host.decision_window(state)
    require(window is not None and window["mode"] == "sequential", "not sequential")
    offers = host.offers(state, window)
    require(list(offers) == ["player.0"] and len(offers["player.0"]) == 9, "bad offers")
    host.apply_sequential(state, window, "player.0", 4)
    require(state.current_player() == 1, "sequential action did not change actor")
    require(not host.window_is_current(state, window), "old Decision Window remained current")


def verify_simultaneous() -> None:
    # Host scenario: simultaneous_window_atomicity.
    game = pyspiel.load_game("matrix_rps")
    state = game.new_initial_state()
    host = HostProjection(game, Authority("session.simultaneous", "world.simultaneous"))
    window = host.decision_window(state)
    require(window is not None and window["mode"] == "simultaneous", "not simultaneous")
    offers = host.offers(state, window)
    require(
        set(offers) == {"player.0", "player.1"}
        and all(len(player_offers) == 3 for player_offers in offers.values()),
        "joint legal actions were not independently authored",
    )
    host.apply_simultaneous(
        state,
        window,
        {"player.0": 0, "player.1": 2},
    )
    require(state.is_terminal(), "joint action was not applied atomically")
    require(state.history() == [0, 2], "joint action order changed")


def verify_chance_and_hidden_information() -> None:
    # Host scenario: chance_transition_host_owned.
    game = pyspiel.load_game("kuhn_poker")
    state = game.new_initial_state()
    host = HostProjection(game, Authority("session.hidden", "world.hidden"))
    require(state.is_chance_node(), "Kuhn poker did not begin at chance")
    require(host.decision_window(state) is None, "chance became a model Decision Window")
    outcomes = state.chance_outcomes()
    require(
        all(probability >= 0 for _, probability in outcomes)
        and abs(sum(probability for _, probability in outcomes) - 1.0) < 1e-12,
        "chance probabilities are invalid",
    )
    host.apply_chance(state, 0)
    require(state.is_chance_node(), "second private deal was not chance")
    require(host.decision_window(state) is None, "private deal became a model action")

    left = state.child(1)
    right = state.child(2)
    # Host scenario: private_observation_noninterference.
    require(str(left) != str(right), "hidden worlds unexpectedly match")
    left_observation = host.observation(left, 0)
    right_observation = host.observation(right, 0)
    require(
        left_observation == right_observation,
        "actor Observation leaked an opponent private card",
    )
    require(
        host.observation(left, 1) != host.observation(right, 1),
        "card owner could not observe its private card",
    )
    left_window = host.decision_window(left)
    right_window = host.decision_window(right)
    require(
        left_window is not None and left_window == right_window,
        "hidden state changed public decision authority",
    )
    require(
        host.offers(left, left_window) == host.offers(right, right_window),
        "hidden state changed public legal offers",
    )


def main() -> None:
    installed = importlib.metadata.version("open-spiel")
    require(
        installed == OPEN_SPIEL_VERSION,
        f"expected OpenSpiel {OPEN_SPIEL_VERSION}, got {installed}",
    )
    verify_sequential()
    verify_simultaneous()
    verify_chance_and_hidden_information()
    print(f"OpenSpiel host semantics verified with {installed}")


if __name__ == "__main__":
    main()
