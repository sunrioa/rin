package io.github.sunrioa.rin.companion;

import net.fabricmc.api.ModInitializer;
import net.fabricmc.fabric.api.event.lifecycle.v1.ServerLifecycleEvents;

public final class AiCompanionMod implements ModInitializer {
    @Override
    public void onInitialize() {
        CompanionEntities.register();
        ServerLifecycleEvents.SERVER_STOPPING.register(CompanionRuntime::close);
    }
}
