package io.github.sunrioa.rin.companion.client;

import io.github.sunrioa.rin.companion.CompanionEntities;
import net.fabricmc.api.ClientModInitializer;
import net.minecraft.client.renderer.entity.EntityRenderers;

public final class AiCompanionClient implements ClientModInitializer {
    @Override
    public void onInitializeClient() {
        EntityRenderers.register(CompanionEntities.TYPE, CompanionRenderer::new);
    }
}
