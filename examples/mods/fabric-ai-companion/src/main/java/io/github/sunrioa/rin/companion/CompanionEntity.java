package io.github.sunrioa.rin.companion;

import net.minecraft.network.syncher.EntityDataAccessor;
import net.minecraft.network.syncher.EntityDataSerializers;
import net.minecraft.network.syncher.SynchedEntityData;
import net.minecraft.server.level.ServerLevel;
import net.minecraft.world.entity.EntityType;
import net.minecraft.world.entity.PathfinderMob;
import net.minecraft.world.entity.ai.attributes.AttributeSupplier;
import net.minecraft.world.entity.ai.attributes.Attributes;
import net.minecraft.world.entity.player.Player;
import net.minecraft.world.level.Level;
import net.minecraft.world.level.storage.ValueInput;
import net.minecraft.world.level.storage.ValueOutput;

import java.util.UUID;

public final class CompanionEntity extends PathfinderMob {
    private static final EntityDataAccessor<String> OWNER_ID = SynchedEntityData.defineId(
            CompanionEntity.class, EntityDataSerializers.STRING);
    private static final EntityDataAccessor<String> MODE = SynchedEntityData.defineId(
            CompanionEntity.class, EntityDataSerializers.STRING);
    private static final double MAX_FOLLOW_DISTANCE = 64.0;
    private static final double STOP_DISTANCE = 3.0;

    enum Mode { STOPPED, FOLLOW }

    CompanionEntity(EntityType<? extends CompanionEntity> type, Level level) {
        super(type, level);
        setPersistenceRequired();
    }

    static AttributeSupplier.Builder createAttributes() {
        return PathfinderMob.createMobAttributes()
                .add(Attributes.MAX_HEALTH, 20.0)
                .add(Attributes.MOVEMENT_SPEED, 0.28)
                .add(Attributes.FOLLOW_RANGE, 32.0)
                .add(Attributes.ATTACK_DAMAGE, 2.0);
    }

    @Override
    protected void defineSynchedData(SynchedEntityData.Builder builder) {
        super.defineSynchedData(builder);
        builder.define(OWNER_ID, "");
        builder.define(MODE, Mode.STOPPED.name());
    }

    public UUID ownerId() {
        String value = entityData.get(OWNER_ID);
        return value.isBlank() ? null : UUID.fromString(value);
    }

    void setOwnerId(UUID ownerId) {
        entityData.set(OWNER_ID, ownerId == null ? "" : ownerId.toString());
    }

    Mode mode() {
        try {
            return Mode.valueOf(entityData.get(MODE));
        } catch (IllegalArgumentException exception) {
            return Mode.STOPPED;
        }
    }

    void setMode(Mode mode) {
        entityData.set(MODE, mode.name());
        if (mode == Mode.STOPPED) {
            getNavigation().stop();
        }
    }

    @Override
    protected void customServerAiStep(ServerLevel level) {
        if (mode() != Mode.FOLLOW) {
            getNavigation().stop();
            return;
        }
        UUID ownerId = ownerId();
        Player owner = ownerId == null ? null : level.getPlayerInAnyDimension(ownerId);
        if (owner == null || owner.level() != level || !owner.isAlive() || distanceToSqr(owner) > MAX_FOLLOW_DISTANCE * MAX_FOLLOW_DISTANCE) {
            getNavigation().stop();
            return;
        }
        if (distanceToSqr(owner) <= STOP_DISTANCE * STOP_DISTANCE) {
            getNavigation().stop();
        } else if (tickCount % 10 == 0) {
            getNavigation().moveTo(owner, 1.0);
        }
    }

    @Override
    protected void addAdditionalSaveData(ValueOutput output) {
        UUID ownerId = ownerId();
        if (ownerId != null) output.putString("owner_id", ownerId.toString());
        output.putString("mode", mode().name());
    }

    @Override
    protected void readAdditionalSaveData(ValueInput input) {
        input.getString("owner_id").ifPresent(value -> {
            try {
                setOwnerId(UUID.fromString(value));
            } catch (IllegalArgumentException ignored) {
                setOwnerId(null);
            }
        });
        input.getString("mode").ifPresent(value -> {
            try {
                setMode(Mode.valueOf(value));
            } catch (IllegalArgumentException ignored) {
                setMode(Mode.STOPPED);
            }
        });
    }
}
