package io.github.sunrioa.rin.companion;

import net.fabricmc.api.ModInitializer;
import net.fabricmc.fabric.api.event.lifecycle.v1.ServerLifecycleEvents;
import net.fabricmc.fabric.api.message.v1.ServerMessageEvents;

public final class AiCompanionMod implements ModInitializer {
    @Override
    public void onInitialize() {
        CompanionEntities.register();
        CompanionCommands.register();
        ServerMessageEvents.ALLOW_CHAT_MESSAGE.register((message, player, parameters) -> {
            var parsed = CompanionChat.parse(message.signedContent());
            parsed.ifPresent(text -> CompanionRuntime.forServer(player.level().getServer()).handleChat(player, text));
            return parsed.isEmpty();
        });
        ServerLifecycleEvents.SERVER_STOPPING.register(CompanionRuntime::close);
    }
}
