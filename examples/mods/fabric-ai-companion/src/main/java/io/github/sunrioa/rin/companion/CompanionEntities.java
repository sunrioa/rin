package io.github.sunrioa.rin.companion;

import net.fabricmc.fabric.api.object.builder.v1.entity.FabricDefaultAttributeRegistry;
import net.minecraft.core.Registry;
import net.minecraft.core.registries.BuiltInRegistries;
import net.minecraft.core.registries.Registries;
import net.minecraft.resources.Identifier;
import net.minecraft.resources.ResourceKey;
import net.minecraft.world.entity.EntityType;
import net.minecraft.world.entity.MobCategory;

public final class CompanionEntities {
    private static final Identifier ID = Identifier.fromNamespaceAndPath("rin_ai_companion", "companion");
    private static final ResourceKey<EntityType<?>> KEY = ResourceKey.create(Registries.ENTITY_TYPE, ID);
    public static final EntityType<CompanionEntity> TYPE = EntityType.Builder.of(CompanionEntity::new, MobCategory.CREATURE)
            .sized(0.6f, 1.8f)
            .clientTrackingRange(10)
            .build(KEY);

    private CompanionEntities() {
    }

    static void register() {
        Registry.register(BuiltInRegistries.ENTITY_TYPE, KEY, TYPE);
        FabricDefaultAttributeRegistry.register(TYPE, CompanionEntity.createAttributes());
    }
}
